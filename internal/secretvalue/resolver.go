package secretvalue

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/privateaccess"
)

const ProviderIBMCloudSecretsManager = "ibm_cloud_secrets_manager"

type Store interface {
	GetSecret(context.Context, string) (core.Secret, error)
	GetSecretStore(context.Context, string) (core.SecretStore, error)
	GetPrivateNetwork(context.Context, string) (core.PrivateNetwork, error)
}

type tokenEntry struct {
	value     string
	expiresAt time.Time
}

// Resolver returns plaintext only to the runtime operation that requested it.
// External values are never copied into Dispatch storage.
type Resolver struct {
	Store  Store
	Vault  *secretcrypto.Vault
	Client *http.Client
	Edge   *edge.Broker

	mu     sync.Mutex
	tokens map[string]tokenEntry
}

func New(data Store, vault *secretcrypto.Vault) *Resolver {
	return &Resolver{Store: data, Vault: vault, Client: &http.Client{Timeout: 20 * time.Second}, tokens: map[string]tokenEntry{}}
}

func (r *Resolver) Resolve(ctx context.Context, id string) ([]byte, error) {
	if r == nil || r.Store == nil {
		return nil, errors.New("secret resolver is not configured")
	}
	secret, err := r.Store.GetSecret(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load secret: %w", err)
	}
	if secret.Source == "" || secret.Source == core.SecretSourceLocal {
		if r.Vault == nil {
			return nil, errors.New("local secret storage is not configured")
		}
		value, err := r.Vault.Decrypt("secret:"+secret.ID, secret.EncryptedValue)
		if err != nil {
			return nil, fmt.Errorf("decrypt secret: %w", err)
		}
		return value, nil
	}
	if secret.Source != core.SecretSourceExternal {
		return nil, fmt.Errorf("unsupported secret source %q", secret.Source)
	}
	store, err := r.Store.GetSecretStore(ctx, secret.ExternalStoreID)
	if err != nil {
		return nil, fmt.Errorf("load external secret store: %w", err)
	}
	credentials, err := r.credentials(store)
	if err != nil {
		return nil, err
	}
	return r.resolveExternal(ctx, store, credentials, secret.ExternalSecretID, secret.ExternalField)
}

func (r *Resolver) Verify(ctx context.Context, store core.SecretStore, credentials map[string]string) error {
	switch store.Provider {
	case ProviderIBMCloudSecretsManager:
		token, err := r.ibmIAMToken(ctx, store, credentials)
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(store.Config["serviceUrl"], "/")+"/api/v2/secrets?limit=1", nil)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Accept", "application/json")
		response, err := r.do(ctx, store, request)
		if err != nil {
			return fmt.Errorf("connect to IBM Cloud Secrets Manager: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return providerResponseError("IBM Cloud Secrets Manager", response)
		}
		return nil
	default:
		return fmt.Errorf("unsupported secret provider %q", store.Provider)
	}
}

func (r *Resolver) VerifyStored(ctx context.Context, store core.SecretStore) error {
	credentials, err := r.credentials(store)
	if err != nil {
		return err
	}
	return r.Verify(ctx, store, credentials)
}

func (r *Resolver) resolveExternal(ctx context.Context, store core.SecretStore, credentials map[string]string, secretID, field string) ([]byte, error) {
	switch store.Provider {
	case ProviderIBMCloudSecretsManager:
		return r.resolveIBM(ctx, store, credentials, secretID, field)
	default:
		return nil, fmt.Errorf("unsupported secret provider %q", store.Provider)
	}
}

func (r *Resolver) resolveIBM(ctx context.Context, store core.SecretStore, credentials map[string]string, secretID, field string) ([]byte, error) {
	token, err := r.ibmIAMToken(ctx, store, credentials)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(store.Config["serviceUrl"], "/") + "/api/v2/secrets/" + url.PathEscape(secretID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := r.do(ctx, store, request)
	if err != nil {
		return nil, fmt.Errorf("retrieve IBM Cloud secret: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, providerResponseError("IBM Cloud Secrets Manager", response)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, errors.New("IBM Cloud Secrets Manager returned invalid JSON")
	}
	value, err := selectValue(document, field)
	if err != nil {
		return nil, err
	}
	return []byte(value), nil
}

func (r *Resolver) credentials(store core.SecretStore) (map[string]string, error) {
	if r.Vault == nil {
		return nil, errors.New("secret store credentials are not configured")
	}
	plaintext, err := r.Vault.Decrypt("secret-store:"+store.ID, store.EncryptedCredentials)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret store credentials: %w", err)
	}
	defer clear(plaintext)
	credentials := map[string]string{}
	if err := json.Unmarshal(plaintext, &credentials); err != nil {
		return nil, errors.New("secret store credentials are invalid")
	}
	return credentials, nil
}

func (r *Resolver) ibmIAMToken(ctx context.Context, store core.SecretStore, credentials map[string]string) (string, error) {
	apiKey := strings.TrimSpace(credentials["apiKey"])
	if apiKey == "" {
		return "", errors.New("IBM Cloud API key is missing")
	}
	cacheKey := fmt.Sprintf("%s:%x", store.ID, sha256.Sum256([]byte(apiKey)))
	r.mu.Lock()
	entry := r.tokens[cacheKey]
	if entry.value != "" && time.Until(entry.expiresAt) > time.Minute {
		r.mu.Unlock()
		return entry.value, nil
	}
	r.mu.Unlock()

	iamURL := strings.TrimSpace(store.Config["iamUrl"])
	if iamURL == "" {
		iamURL = "https://iam.cloud.ibm.com"
	}
	form := url.Values{"grant_type": {"urn:ibm:params:oauth:grant-type:apikey"}, "apikey": {apiKey}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(iamURL, "/")+"/identity/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := r.do(ctx, store, request)
	if err != nil {
		return "", fmt.Errorf("authenticate with IBM Cloud IAM: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", providerResponseError("IBM Cloud IAM", response)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		Expiration  int64  `json:"expiration"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&token); err != nil || token.AccessToken == "" {
		return "", errors.New("IBM Cloud IAM returned an invalid token")
	}
	expiresAt := time.Unix(token.Expiration, 0)
	if token.Expiration == 0 {
		expiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	r.mu.Lock()
	r.tokens[cacheKey] = tokenEntry{value: token.AccessToken, expiresAt: expiresAt}
	r.mu.Unlock()
	return token.AccessToken, nil
}

func (r *Resolver) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (r *Resolver) do(ctx context.Context, store core.SecretStore, request *http.Request) (*http.Response, error) {
	networkID := strings.TrimSpace(store.Config["privateNetworkId"])
	if networkID != "" {
		network, err := r.Store.GetPrivateNetwork(ctx, networkID)
		if err != nil {
			return nil, fmt.Errorf("load network route: %w", err)
		}
		if network.Driver == edge.DriverAgent {
			if r.Edge == nil {
				return nil, errors.New("edge routing is not configured")
			}
			return r.Edge.Do(ctx, network, request)
		}
	}
	client, err := r.clientForStore(ctx, store, request.URL)
	if err != nil {
		return nil, err
	}
	return client.Do(request)
}

func (r *Resolver) clientForStore(ctx context.Context, store core.SecretStore, endpoint *url.URL) (*http.Client, error) {
	networkID := strings.TrimSpace(store.Config["privateNetworkId"])
	if networkID == "" {
		return r.client(), nil
	}
	network, err := r.Store.GetPrivateNetwork(ctx, networkID)
	if err != nil {
		return nil, fmt.Errorf("load private network: %w", err)
	}
	if network.Driver != privateaccess.DriverLaneway {
		return nil, fmt.Errorf("unsupported private network driver %q", network.Driver)
	}
	snapshot, err := privateaccess.CheckLaneway(ctx, network.Config["socketPath"])
	if err != nil {
		return nil, fmt.Errorf("private network %q is unavailable: %w", network.Name, err)
	}
	address := endpointAddress(store, endpoint)
	if address == "" {
		return r.client(), nil
	}
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return nil, fmt.Errorf("private endpoint address %q is not an IP address", address)
	}
	if !privateaccess.RouteCovers(snapshot.Routes, ip) {
		return nil, fmt.Errorf("Laneway has no route to %s", ip)
	}
	base := r.client()
	transport, ok := base.Transport.(*http.Transport)
	if base.Transport == nil {
		transport = http.DefaultTransport.(*http.Transport)
		ok = true
	}
	if !ok {
		return nil, errors.New("private endpoint routing requires an HTTP transport")
	}
	clone := transport.Clone()
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	expectedHost := endpoint.Hostname()
	clone.DialContext = func(ctx context.Context, network, target string) (net.Conn, error) {
		host, port, splitErr := net.SplitHostPort(target)
		if splitErr == nil && strings.EqualFold(host, expectedHost) {
			target = net.JoinHostPort(ip.String(), port)
		}
		return dialer.DialContext(ctx, network, target)
	}
	return &http.Client{Transport: clone, Timeout: base.Timeout}, nil
}

func endpointAddress(store core.SecretStore, endpoint *url.URL) string {
	serviceURL, _ := url.Parse(store.Config["serviceUrl"])
	if serviceURL != nil && strings.EqualFold(serviceURL.Hostname(), endpoint.Hostname()) {
		return strings.TrimSpace(store.Config["serviceAddress"])
	}
	iamURL, _ := url.Parse(store.Config["iamUrl"])
	if iamURL != nil && strings.EqualFold(iamURL.Hostname(), endpoint.Hostname()) {
		return strings.TrimSpace(store.Config["iamAddress"])
	}
	return ""
}

func ValidateStore(store core.SecretStore) error {
	if store.Provider != ProviderIBMCloudSecretsManager {
		return errors.New("choose a supported secret provider")
	}
	if err := validateEndpoint(store.Config["serviceUrl"], "service URL"); err != nil {
		return err
	}
	if err := validateEndpoint(store.Config["iamUrl"], "IAM URL"); err != nil {
		return err
	}
	if store.Config["privateNetworkId"] == "" && (store.Config["serviceAddress"] != "" || store.Config["iamAddress"] != "") {
		return errors.New("choose a private network before setting private endpoint addresses")
	}
	for key, label := range map[string]string{"serviceAddress": "service address", "iamAddress": "IAM address"} {
		if value := strings.TrimSpace(store.Config[key]); value != "" {
			if _, err := netip.ParseAddr(value); err != nil {
				return fmt.Errorf("enter a valid %s", label)
			}
		}
	}
	return nil
}

func validateEndpoint(value, label string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("enter a valid %s", label)
	}
	if parsed.Scheme == "http" {
		host := parsed.Hostname()
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("%s must use HTTPS", label)
		}
	}
	return nil
}

func selectValue(document any, field string) (string, error) {
	current := document
	if strings.TrimSpace(field) != "" {
		var found bool
		current, found = lookupField(document, field)
		if !found {
			if object, ok := document.(map[string]any); ok {
				if payload, ok := object["payload"].(string); ok {
					var decoded any
					if json.Unmarshal([]byte(payload), &decoded) == nil {
						current, found = lookupField(decoded, field)
					}
				}
			}
		}
		if !found {
			return "", fmt.Errorf("external secret field %q was not found", field)
		}
	} else if object, ok := current.(map[string]any); ok {
		for _, key := range []string{"payload", "value", "api_key", "password", "data"} {
			if candidate, exists := object[key]; exists {
				current = candidate
				break
			}
		}
	}
	switch value := current.(type) {
	case string:
		return value, nil
	case nil:
		return "", errors.New("external secret has no value")
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", errors.New("external secret value could not be encoded")
		}
		return string(encoded), nil
	}
}

func lookupField(document any, field string) (any, bool) {
	current := document
	for _, part := range strings.Split(field, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func providerResponseError(provider string, response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
	var problem struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(body, &problem)
	detail := strings.TrimSpace(problem.Message)
	if detail == "" && len(problem.Errors) > 0 {
		detail = strings.TrimSpace(problem.Errors[0].Message)
	}
	if detail == "" {
		detail = http.StatusText(response.StatusCode)
	}
	return fmt.Errorf("%s request failed: %s", provider, detail)
}
