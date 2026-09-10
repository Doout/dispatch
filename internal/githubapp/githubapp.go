package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/edge"
)

type Store interface {
	GetGitHubApp(context.Context, string) (core.GitHubAppConnection, error)
	GetPrivateNetwork(context.Context, string) (core.PrivateNetwork, error)
}

type cachedToken struct {
	value     string
	expiresAt time.Time
}

type Manager struct {
	Store  Store
	Vault  *secretcrypto.Vault
	Client *http.Client
	Edge   *edge.Broker

	mu     sync.Mutex
	tokens map[string]cachedToken
}

type ManifestConversion struct {
	AppID                 int64
	ClientID              string
	ClientSecret          string
	Name                  string
	Slug                  string
	RegistrationOwner     string
	RegistrationOwnerType string
	PrivateKey            string
	WebhookSecret         string
}

type Installation struct {
	ID      int64  `json:"id"`
	Account string `json:"account"`
	Target  string `json:"target"`
}

// Repository is a repository selected for one GitHub App installation. The
// installation token only returns repositories that the App can access.
type Repository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"fullName"`
	Name          string `json:"name"`
	Owner         string `json:"owner"`
	DefaultBranch string `json:"defaultBranch"`
	Private       bool   `json:"private"`
	WebURL        string `json:"webUrl"`
}

type RepositoryFile struct {
	Path     string `json:"path"`
	Contents []byte `json:"-"`
}

type Verification struct {
	Slug                  string `json:"slug"`
	ClientID              string `json:"clientId"`
	RegistrationOwner     string `json:"registrationOwner"`
	RegistrationOwnerType string `json:"registrationOwnerType"`
	InstallationAccount   string `json:"installationAccount"`
	InstallationURL       string `json:"installationUrl"`
	RepositorySelection   string `json:"repositorySelection"`
	RepositoryCount       int    `json:"repositoryCount"`
	PushSubscribed        bool   `json:"pushSubscribed"`
}

func New(store Store, vault *secretcrypto.Vault) *Manager {
	return &Manager{Store: store, Vault: vault, tokens: map[string]cachedToken{}}
}

func (m *Manager) Invalidate(id string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.tokens, id)
	m.mu.Unlock()
}

func NormalizeEndpoints(webURL, apiURL string) (string, string, error) {
	webURL = strings.TrimRight(strings.TrimSpace(webURL), "/")
	if webURL == "" {
		webURL = "https://github.com"
	}
	parsed, err := url.Parse(webURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", errors.New("enter a valid HTTPS GitHub URL")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", "", errors.New("GitHub URL must not include a path")
	}
	parsed.Path = ""
	webURL = parsed.String()
	apiURL = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	if apiURL == "" {
		if strings.EqualFold(parsed.Host, "github.com") {
			apiURL = "https://api.github.com"
		} else {
			apiURL = webURL + "/api/v3"
		}
	}
	api, err := url.Parse(apiURL)
	if err != nil || api.Scheme != "https" || api.Host == "" || api.User != nil {
		return "", "", errors.New("enter a valid HTTPS GitHub API URL")
	}
	return webURL, apiURL, nil
}

// ValidateRepositoryHost prevents a connection registered on one GitHub host
// from being selected for a repository on another host.
func ValidateRepositoryHost(repository, webURL string) error {
	repo, err := url.Parse(strings.TrimSpace(repository))
	if err != nil || !strings.EqualFold(repo.Scheme, "https") || repo.Host == "" {
		return errors.New("GitHub Apps require a valid HTTPS repository URL")
	}
	github, err := url.Parse(strings.TrimSpace(webURL))
	if err != nil || github.Host == "" {
		return errors.New("GitHub App has an invalid GitHub URL")
	}
	if !strings.EqualFold(repo.Host, github.Host) {
		return fmt.Errorf("repository host %s does not match GitHub App host %s", repo.Host, github.Host)
	}
	return nil
}

func ParsePrivateKey(value string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(value)))
	if block == nil {
		return nil, errors.New("private key must be a PEM file generated for the GitHub App")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("private key is not a supported RSA PEM key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GitHub App private key must use RSA")
	}
	return key, nil
}

func (m *Manager) EncryptCredentials(id, privateKey, webhookSecret string) (string, string, error) {
	if m == nil || m.Vault == nil {
		return "", "", errors.New("encrypted credential storage is not configured")
	}
	if _, err := ParsePrivateKey(privateKey); err != nil {
		return "", "", err
	}
	if len(strings.TrimSpace(webhookSecret)) < 16 {
		return "", "", errors.New("webhook secret must contain at least 16 characters")
	}
	privateEncrypted, err := m.Vault.Encrypt("github-app:"+id+":private-key", []byte(strings.TrimSpace(privateKey)))
	if err != nil {
		return "", "", err
	}
	webhookEncrypted, err := m.Vault.Encrypt("github-app:"+id+":webhook-secret", []byte(webhookSecret))
	if err != nil {
		return "", "", err
	}
	return privateEncrypted, webhookEncrypted, nil
}

func (m *Manager) WebhookSecret(ctx context.Context, id string) (string, error) {
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return "", err
	}
	value, err := m.Vault.Decrypt("github-app:"+id+":webhook-secret", connection.EncryptedWebhookSecret)
	if err != nil {
		return "", fmt.Errorf("decrypt GitHub App webhook secret: %w", err)
	}
	return string(value), nil
}

func (m *Manager) InstallationToken(ctx context.Context, id string) (string, error) {
	m.mu.Lock()
	if cached := m.tokens[id]; cached.value != "" && time.Until(cached.expiresAt) > 5*time.Minute {
		m.mu.Unlock()
		return cached.value, nil
	}
	m.mu.Unlock()
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return "", err
	}
	if connection.InstallationID < 1 {
		return "", errors.New("GitHub App is not linked to an installation")
	}
	jwt, err := m.appJWT(connection)
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("%s/app/installations/%d/access_tokens", strings.TrimRight(connection.APIURL, "/"), connection.InstallationID)
	var response struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := m.request(ctx, http.MethodPost, endpoint, jwt, nil, &response, connection.PrivateNetworkID); err != nil {
		return "", fmt.Errorf("create GitHub App installation token: %w", err)
	}
	if response.Token == "" {
		return "", errors.New("GitHub returned an empty installation token")
	}
	m.mu.Lock()
	m.tokens[id] = cachedToken{value: response.Token, expiresAt: response.ExpiresAt}
	m.mu.Unlock()
	return response.Token, nil
}

func (m *Manager) ListInstallations(ctx context.Context, id string) ([]Installation, error) {
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return nil, err
	}
	jwt, err := m.appJWT(connection)
	if err != nil {
		return nil, err
	}
	var response []struct {
		ID      int64  `json:"id"`
		Target  string `json:"target_type"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	endpoint := strings.TrimRight(connection.APIURL, "/") + "/app/installations?per_page=100"
	if err := m.request(ctx, http.MethodGet, endpoint, jwt, nil, &response, connection.PrivateNetworkID); err != nil {
		return nil, err
	}
	items := make([]Installation, 0, len(response))
	for _, item := range response {
		items = append(items, Installation{ID: item.ID, Account: item.Account.Login, Target: item.Target})
	}
	return items, nil
}

func (m *Manager) ListRepositories(ctx context.Context, id string) ([]Repository, error) {
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return nil, err
	}
	token, err := m.InstallationToken(ctx, id)
	if err != nil {
		return nil, err
	}
	items := []Repository{}
	for page := 1; ; page++ {
		var response struct {
			Repositories []struct {
				ID            int64  `json:"id"`
				FullName      string `json:"full_name"`
				Name          string `json:"name"`
				DefaultBranch string `json:"default_branch"`
				Private       bool   `json:"private"`
				HTMLURL       string `json:"html_url"`
				Owner         struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"repositories"`
		}
		endpoint := fmt.Sprintf("%s/installation/repositories?per_page=100&page=%d", strings.TrimRight(connection.APIURL, "/"), page)
		if err := m.request(ctx, http.MethodGet, endpoint, token, nil, &response, connection.PrivateNetworkID); err != nil {
			return nil, fmt.Errorf("list installation repositories: %w", err)
		}
		for _, repository := range response.Repositories {
			items = append(items, Repository{ID: repository.ID, FullName: repository.FullName, Name: repository.Name,
				Owner: repository.Owner.Login, DefaultBranch: repository.DefaultBranch, Private: repository.Private, WebURL: repository.HTMLURL})
		}
		if len(response.Repositories) < 100 {
			break
		}
	}
	return items, nil
}

// RepositoryHead resolves a branch to an immutable commit. Both webhook and
// polling reconciliation use this method so they produce the same snapshots.
func (m *Manager) RepositoryHead(ctx context.Context, id, repository, branch string) (string, error) {
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return "", err
	}
	token, err := m.InstallationToken(ctx, id)
	if err != nil {
		return "", err
	}
	repository, err = repositoryPath(repository)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(branch) == "" {
		branch = "main"
	}
	var response struct {
		SHA string `json:"sha"`
	}
	endpoint := fmt.Sprintf("%s/repos/%s/commits/%s", strings.TrimRight(connection.APIURL, "/"), repository, url.PathEscape(branch))
	if err := m.request(ctx, http.MethodGet, endpoint, token, nil, &response, connection.PrivateNetworkID); err != nil {
		return "", fmt.Errorf("resolve %s branch %s: %w", repository, branch, err)
	}
	if strings.TrimSpace(response.SHA) == "" {
		return "", errors.New("GitHub returned an empty commit")
	}
	return response.SHA, nil
}

// RepositoryFiles returns YAML or JSON configuration files at an immutable
// revision. path may identify one file or a directory prefix.
var ErrNoConfigurationFiles = errors.New("no YAML or JSON configuration files found")

func (m *Manager) RepositoryFiles(ctx context.Context, id, repository, revision, path string) ([]RepositoryFile, error) {
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return nil, err
	}
	token, err := m.InstallationToken(ctx, id)
	if err != nil {
		return nil, err
	}
	repository, err = repositoryPath(repository)
	if err != nil {
		return nil, err
	}
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		path = ".dispatch"
	}
	var tree struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
			Size int64  `json:"size"`
		} `json:"tree"`
	}
	endpoint := fmt.Sprintf("%s/repos/%s/git/trees/%s?recursive=1", strings.TrimRight(connection.APIURL, "/"), repository, url.PathEscape(revision))
	if err := m.request(ctx, http.MethodGet, endpoint, token, nil, &tree, connection.PrivateNetworkID); err != nil {
		return nil, fmt.Errorf("list configuration files: %w", err)
	}
	if tree.Truncated {
		return nil, errors.New("repository tree is too large to inspect; configure a narrower path")
	}
	type blob struct{ path, sha string }
	blobs := []blob{}
	for _, entry := range tree.Tree {
		if entry.Type != "blob" || !configurationPath(path, entry.Path) {
			continue
		}
		if entry.Mode == "120000" {
			return nil, fmt.Errorf("configuration file %s is a symbolic link", entry.Path)
		}
		if entry.Size > 1<<20 {
			return nil, fmt.Errorf("configuration file %s exceeds 1 MiB", entry.Path)
		}
		blobs = append(blobs, blob{path: entry.Path, sha: entry.SHA})
	}
	if len(blobs) == 0 {
		return nil, fmt.Errorf("%w at %s", ErrNoConfigurationFiles, path)
	}
	if len(blobs) > 64 {
		return nil, errors.New("configuration path contains more than 64 YAML or JSON files")
	}
	items := make([]RepositoryFile, 0, len(blobs))
	for _, item := range blobs {
		var response struct {
			Encoding string `json:"encoding"`
			Content  string `json:"content"`
			Size     int64  `json:"size"`
		}
		endpoint := fmt.Sprintf("%s/repos/%s/git/blobs/%s", strings.TrimRight(connection.APIURL, "/"), repository, url.PathEscape(item.sha))
		if err := m.request(ctx, http.MethodGet, endpoint, token, nil, &response, connection.PrivateNetworkID); err != nil {
			return nil, fmt.Errorf("load configuration file %s: %w", item.path, err)
		}
		if response.Encoding != "base64" || response.Size > 1<<20 {
			return nil, fmt.Errorf("configuration file %s has an unsupported encoding or size", item.path)
		}
		contents, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(response.Content, "\n", ""))
		if err != nil {
			return nil, fmt.Errorf("decode configuration file %s: %w", item.path, err)
		}
		items = append(items, RepositoryFile{Path: item.path, Contents: contents})
	}
	return items, nil
}

func (m *Manager) SetCommitStatus(ctx context.Context, id, repository, revision, state, description, targetURL string) error {
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return err
	}
	token, err := m.InstallationToken(ctx, id)
	if err != nil {
		return err
	}
	repository, err = repositoryPath(repository)
	if err != nil {
		return err
	}
	payload := map[string]string{
		"state":       state,
		"context":     "Dispatch/deployment",
		"description": description,
	}
	if strings.TrimSpace(targetURL) != "" {
		payload["target_url"] = targetURL
	}
	endpoint := fmt.Sprintf("%s/repos/%s/statuses/%s", strings.TrimRight(connection.APIURL, "/"), repository, url.PathEscape(revision))
	if err := m.request(ctx, http.MethodPost, endpoint, token, payload, nil, connection.PrivateNetworkID); err != nil {
		return fmt.Errorf("publish commit status: %w", err)
	}
	return nil
}

func repositoryPath(repository string) (string, error) {
	repository = strings.Trim(strings.TrimSpace(repository), "/")
	if parsed, err := url.Parse(repository); err == nil && parsed.Host != "" {
		repository = strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" || strings.Contains(repository, "..") {
		return "", errors.New("repository must use owner/name")
	}
	return url.PathEscape(parts[0]) + "/" + url.PathEscape(strings.TrimSuffix(parts[1], ".git")), nil
}

func configurationPath(root, candidate string) bool {
	lower := strings.ToLower(candidate)
	if !strings.HasSuffix(lower, ".yaml") && !strings.HasSuffix(lower, ".yml") && !strings.HasSuffix(lower, ".json") {
		return false
	}
	return candidate == root || strings.HasPrefix(candidate, strings.TrimRight(root, "/")+"/")
}

func (m *Manager) Verify(ctx context.Context, id string) (Verification, error) {
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return Verification{}, err
	}
	jwt, err := m.appJWT(connection)
	if err != nil {
		return Verification{}, err
	}
	var app struct {
		ID       int64  `json:"id"`
		Slug     string `json:"slug"`
		ClientID string `json:"client_id"`
		Owner    struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"owner"`
		Events []string `json:"events"`
	}
	if err := m.request(ctx, http.MethodGet, strings.TrimRight(connection.APIURL, "/")+"/app", jwt, nil, &app, connection.PrivateNetworkID); err != nil {
		return Verification{}, fmt.Errorf("verify GitHub App registration: %w", err)
	}
	if app.ID != connection.AppID {
		return Verification{}, fmt.Errorf("private key belongs to GitHub App %d, not %d", app.ID, connection.AppID)
	}
	result := Verification{Slug: app.Slug, ClientID: app.ClientID, RegistrationOwner: app.Owner.Login, RegistrationOwnerType: app.Owner.Type}
	result.PushSubscribed = slices.Contains(app.Events, "push")
	if connection.InstallationID < 1 {
		return result, nil
	}
	var installation struct {
		ID                  int64  `json:"id"`
		HTMLURL             string `json:"html_url"`
		RepositorySelection string `json:"repository_selection"`
		Account             struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	endpoint := fmt.Sprintf("%s/app/installations/%d", strings.TrimRight(connection.APIURL, "/"), connection.InstallationID)
	if err := m.request(ctx, http.MethodGet, endpoint, jwt, nil, &installation, connection.PrivateNetworkID); err != nil {
		return Verification{}, fmt.Errorf("verify GitHub App installation: %w", err)
	}
	if installation.ID != connection.InstallationID {
		return Verification{}, errors.New("GitHub returned a different installation")
	}
	result.InstallationAccount = installation.Account.Login
	result.InstallationURL = installation.HTMLURL
	result.RepositorySelection = installation.RepositorySelection
	token, err := m.InstallationToken(ctx, id)
	if err != nil {
		return Verification{}, err
	}
	var repositories struct {
		Total int `json:"total_count"`
	}
	if err := m.request(ctx, http.MethodGet, strings.TrimRight(connection.APIURL, "/")+"/installation/repositories?per_page=1", token, nil, &repositories, connection.PrivateNetworkID); err != nil {
		return Verification{}, fmt.Errorf("verify installation repository access: %w", err)
	}
	result.RepositoryCount = repositories.Total
	return result, nil
}

// EnsureWebhookConfig makes the registered GitHub App deliver to the route
// owned by this connection. Event subscriptions remain part of the App
// registration; App manifests created by Dispatch include push events.
func (m *Manager) EnsureWebhookConfig(ctx context.Context, id string) error {
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(connection.WebhookURL) == "" {
		return errors.New("webhook delivery is disabled for this GitHub App")
	}
	jwt, err := m.appJWT(connection)
	if err != nil {
		return err
	}
	secret, err := m.WebhookSecret(ctx, id)
	if err != nil {
		return err
	}
	payload := map[string]string{
		"url":          connection.WebhookURL,
		"content_type": "json",
		"secret":       secret,
		"insecure_ssl": "0",
	}
	endpoint := strings.TrimRight(connection.APIURL, "/") + "/app/hook/config"
	if err := m.request(ctx, http.MethodPatch, endpoint, jwt, payload, nil, connection.PrivateNetworkID); err != nil {
		return fmt.Errorf("configure GitHub App webhook: %w", err)
	}
	return nil
}

func (m *Manager) ConvertManifest(ctx context.Context, apiURL, code string, privateNetworkID ...string) (ManifestConversion, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(apiURL), "/") + "/app-manifests/" + url.PathEscape(strings.TrimSpace(code)) + "/conversions"
	var result struct {
		ID            int64  `json:"id"`
		ClientID      string `json:"client_id"`
		ClientSecret  string `json:"client_secret"`
		Name          string `json:"name"`
		Slug          string `json:"slug"`
		PEM           string `json:"pem"`
		WebhookSecret string `json:"webhook_secret"`
		Owner         struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"owner"`
	}
	if err := m.request(ctx, http.MethodPost, endpoint, "", nil, &result, privateNetworkID...); err != nil {
		return ManifestConversion{}, err
	}
	if result.ID < 1 || result.PEM == "" {
		return ManifestConversion{}, errors.New("GitHub returned an incomplete App manifest conversion")
	}
	return ManifestConversion{AppID: result.ID, ClientID: result.ClientID, ClientSecret: result.ClientSecret, Name: result.Name, Slug: result.Slug,
		RegistrationOwner: result.Owner.Login, RegistrationOwnerType: result.Owner.Type,
		PrivateKey: result.PEM, WebhookSecret: result.WebhookSecret}, nil
}

func (m *Manager) appJWT(connection core.GitHubAppConnection) (string, error) {
	if m == nil || m.Vault == nil {
		return "", errors.New("encrypted credential storage is not configured")
	}
	value, err := m.Vault.Decrypt("github-app:"+connection.ID+":private-key", connection.EncryptedPrivateKey)
	if err != nil {
		return "", fmt.Errorf("decrypt GitHub App private key: %w", err)
	}
	key, err := ParsePrivateKey(string(value))
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]interface{}{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": connection.AppID})
	unsigned := rawURL(header) + "." + rawURL(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func rawURL(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }

func (m *Manager) request(ctx context.Context, method, endpoint, token string, body interface{}, output interface{}, privateNetworkID ...string) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	var response *http.Response
	if len(privateNetworkID) > 0 && strings.TrimSpace(privateNetworkID[0]) != "" {
		if m.Store == nil || m.Edge == nil {
			return errors.New("GitHub edge routing is not configured")
		}
		network, loadErr := m.Store.GetPrivateNetwork(ctx, strings.TrimSpace(privateNetworkID[0]))
		if loadErr != nil {
			return fmt.Errorf("load GitHub network route: %w", loadErr)
		}
		response, err = m.Edge.Do(ctx, network, request)
	} else {
		client := m.Client
		if client == nil {
			client = &http.Client{Timeout: 15 * time.Second}
		}
		response, err = client.Do(request)
	}
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		message := strings.TrimSpace(string(detail))
		if message == "" {
			message = response.Status
		}
		return fmt.Errorf("GitHub returned %s: %s", response.Status, message)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func State(connection core.GitHubAppConnection) string {
	if connection.InstallationID < 1 {
		return "needs_installation"
	}
	if connection.LastVerifiedAt == nil {
		return "unverified"
	}
	return "ready"
}
