package repositoryproxy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"golang.org/x/net/http/httpproxy"
)

const credentialScope = "settings:repository-proxy"

var ErrEncryptionUnavailable = errors.New("Configure encrypted storage before saving proxy credentials.")

type Reader interface {
	GetRepositoryProxy(context.Context) (core.RepositoryProxySettings, error)
	GetGitHubApp(context.Context, string) (core.GitHubAppConnection, error)
}

type Service struct {
	Store Reader
	Vault *secretcrypto.Vault
}

func (s *Service) Prepare(appID, value, bypass string) (core.RepositoryProxySettings, error) {
	value = strings.TrimSpace(value)
	if len(value) > 2048 || strings.ContainsFunc(value, unicode.IsSpace) {
		return core.RepositoryProxySettings{}, errors.New("Enter a valid HTTP or HTTPS proxy URL.")
	}
	if len(bypass) > 2048 || strings.ContainsAny(bypass, "\x00\r\n") {
		return core.RepositoryProxySettings{}, errors.New("Enter a comma-separated bypass list under 2048 bytes.")
	}
	settings := core.RepositoryProxySettings{NoProxy: strings.TrimSpace(bypass)}
	if value == "" {
		return settings, nil
	}
	address, err := url.Parse(value)
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Hostname() == "" ||
		(address.Path != "" && address.Path != "/") || address.RawQuery != "" || address.ForceQuery || address.Fragment != "" {
		return core.RepositoryProxySettings{}, errors.New("Enter an HTTP or HTTPS proxy URL with a host and optional port.")
	}
	if address.Port() != "" {
		port, err := strconv.Atoi(address.Port())
		if err != nil || port < 1 || port > 65535 {
			return core.RepositoryProxySettings{}, errors.New("Enter a proxy port between 1 and 65535.")
		}
	}
	if address.User != nil {
		if s.Vault == nil {
			return core.RepositoryProxySettings{}, ErrEncryptionUnavailable
		}
		settings.EncryptedUserinfo, err = s.Vault.Encrypt(proxyCredentialScope(appID), []byte(address.User.String()))
		if err != nil {
			return core.RepositoryProxySettings{}, err
		}
		address.User = nil
	}
	address.Path, address.RawPath = "", ""
	settings.URL = address.String()
	return settings, nil
}

func proxyCredentialScope(appID string) string {
	if appID == "" {
		return credentialScope
	}
	return "github-app:" + appID + ":proxy"
}

type githubAppKey struct{}

type githubAppProxy struct {
	id       string
	settings *core.RepositoryProxySettings
}

func WithGitHubApp(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, githubAppKey{}, githubAppProxy{id: id})
}

func WithGitHubAppConnection(ctx context.Context, connection core.GitHubAppConnection) context.Context {
	if connection.ID == "" {
		return ctx
	}
	return context.WithValue(ctx, githubAppKey{}, githubAppProxy{id: connection.ID, settings: &connection.Proxy})
}

func (s *Service) Resolve(ctx context.Context) (proxyURL, noProxy string, err error) {
	if s == nil {
		return "", "", nil
	}
	if app, ok := ctx.Value(githubAppKey{}).(githubAppProxy); ok {
		settings := app.settings
		if settings == nil {
			connection, err := s.Store.GetGitHubApp(ctx, app.id)
			if err != nil {
				return "", "", fmt.Errorf("load GitHub App proxy: %w", err)
			}
			settings = &connection.Proxy
		}
		if settings.URL != "" {
			return s.resolveSettings(*settings, app.id)
		}
	}
	settings, err := s.Store.GetRepositoryProxy(ctx)
	if err != nil {
		return "", "", fmt.Errorf("load repository proxy: %w", err)
	}
	return s.resolveSettings(settings, "")
}

func (s *Service) resolveSettings(settings core.RepositoryProxySettings, appID string) (string, string, error) {
	if settings.EncryptedUserinfo == "" {
		return settings.URL, settings.NoProxy, nil
	}
	userinfo, err := s.Vault.Decrypt(proxyCredentialScope(appID), settings.EncryptedUserinfo)
	if err != nil {
		return "", "", fmt.Errorf("decrypt repository proxy credentials: %w", err)
	}
	defer clear(userinfo)
	scheme, host, _ := strings.Cut(settings.URL, "://")
	return scheme + "://" + string(userinfo) + "@" + host, settings.NoProxy, nil
}

func (s *Service) Transport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// net/http returns only the proxy's arbitrary reason phrase on failed CONNECT.
	// Keep 407 identifiable even if the proxy supplies a custom status text.
	transport.OnProxyConnectResponse = func(_ context.Context, _ *url.URL, _ *http.Request, response *http.Response) error {
		if response.StatusCode == http.StatusProxyAuthRequired {
			return errors.New("proxy authentication required")
		}
		return nil
	}
	transport.Proxy = func(request *http.Request) (*url.URL, error) {
		selectProxy, err := s.proxyFunc(request.Context())
		if err != nil {
			return nil, err
		}
		if selectProxy == nil {
			return http.ProxyFromEnvironment(request)
		}
		return selectProxy(request.URL)
	}
	return transport
}

type requestProxyKey struct{}

func (s *Service) proxyFunc(ctx context.Context) (func(*url.URL) (*url.URL, error), error) {
	if selectProxy, ok := ctx.Value(requestProxyKey{}).(func(*url.URL) (*url.URL, error)); ok {
		return selectProxy, nil
	}
	proxyURL, noProxy, err := s.Resolve(ctx)
	if err != nil || proxyURL == "" {
		return nil, err
	}
	config := httpproxy.Config{HTTPProxy: proxyURL, HTTPSProxy: proxyURL, NoProxy: noProxy}
	return config.ProxyFunc(), nil
}

// Route returns whether a saved proxy applies to the request. If it does,
// the HTTP transport uses the same settings that selected the route.
func (s *Service) Route(request *http.Request) (*http.Request, bool, error) {
	selectProxy, err := s.proxyFunc(request.Context())
	if err != nil || selectProxy == nil {
		return request, false, err
	}
	address, err := selectProxy(request.URL)
	if err != nil || address == nil {
		return request, false, err
	}
	return request.WithContext(context.WithValue(request.Context(), requestProxyKey{}, selectProxy)), true, nil
}

func GitEnvironment(environment []string, repository, proxyURL, noProxy string) []string {
	if proxyURL == "" || !IsHTTPRepository(repository) {
		return environment
	}
	environment = append(environment, "HTTP_PROXY="+proxyURL, "HTTPS_PROXY="+proxyURL, "NO_PROXY="+noProxy,
		"http_proxy="+proxyURL, "https_proxy="+proxyURL, "no_proxy="+noProxy)
	count := 0
	for i := len(environment) - 1; i >= 0; i-- {
		value, ok := strings.CutPrefix(environment[i], "GIT_CONFIG_COUNT=")
		if ok {
			var err error
			count, err = strconv.Atoi(value)
			if err != nil || count < 0 || count > math.MaxInt-2 {
				return environment
			}
			break
		}
	}
	// URL-specific settings win over http.proxy, and fetching a named remote
	// also consults remote.<name>.proxy. Dispatch fetches from origin.
	for _, key := range []string{"http." + repository + ".proxy", "remote.origin.proxy"} {
		index := strconv.Itoa(count)
		environment = append(environment, "GIT_CONFIG_KEY_"+index+"="+key, "GIT_CONFIG_VALUE_"+index+"="+proxyURL)
		count++
	}
	return append(environment, "GIT_CONFIG_COUNT="+strconv.Itoa(count))
}

func IsHTTPRepository(repository string) bool {
	return strings.HasPrefix(repository, "https://") || strings.HasPrefix(repository, "http://")
}

func RedactGitOutput(output string, environment []string) string {
	for i := len(environment) - 1; i >= 0; i-- {
		raw, ok := strings.CutPrefix(environment[i], "https_proxy=")
		if !ok {
			continue
		}
		if address, err := url.Parse(raw); err == nil && address.User != nil {
			address.User = nil
			return strings.ReplaceAll(output, raw, address.String())
		}
		break
	}
	return output
}
