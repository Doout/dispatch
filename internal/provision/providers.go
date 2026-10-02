// Package provision reconciles external infrastructure through the versioned
// provider contract. It contains no cloud-specific clients or credentials.
package provision

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/bootstrap"
	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

type ProviderStore interface {
	store.InfrastructureProviderStore
	GetSecret(context.Context, string) (core.Secret, error)
	GetPrivateNetwork(context.Context, string) (core.PrivateNetwork, error)
}
type SecretResolver interface {
	Resolve(context.Context, string) ([]byte, error)
}
type Manager struct {
	Bootstrap *bootstrap.Manager
	Store     ProviderStore
	Secrets   SecretResolver
	Edge      *edge.Broker
	// HTTPClient is injectable for certificate-pinned tests and managed transport.
	HTTPClient *http.Client
	Vault      *secretcrypto.Vault
	Authorize  func(context.Context, string, string, string) error
	Admission  store.InfrastructureAdmission
	Now        func() time.Time
}
type Registration struct {
	Name               string   `json:"name"`
	Endpoint           string   `json:"endpoint"`
	PrivateNetworkID   string   `json:"privateNetworkId"`
	CredentialSecretID string   `json:"credentialSecretId"`
	Enabled            bool     `json:"enabled"`
	Capabilities       []string `json:"capabilities"`
	Revision           int64    `json:"revision"`
}

var ErrDisabled = errors.New("provider mutations are disabled; verify and enable the registration")
var ErrIdentity = errors.New("provider identity or manifest changed; review the registration before using it")

func Endpoint(value, network string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(value) > 2048 {
		return "", errors.New("use an HTTPS provider endpoint without embedded credentials, query or fragment")
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback && network == "") {
		return "", errors.New("provider endpoints require HTTPS; HTTP is allowed only for a direct loopback mock")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func (m *Manager) input(ctx context.Context, in Registration) (core.InfrastructureProvider, error) {
	p := core.InfrastructureProvider{Name: strings.TrimSpace(in.Name), PrivateNetworkID: strings.TrimSpace(in.PrivateNetworkID), CredentialSecretID: strings.TrimSpace(in.CredentialSecretID), Enabled: in.Enabled, Capabilities: in.Capabilities}
	if len(p.Name) == 0 || len(p.Name) > 80 {
		return p, errors.New("provider name must contain 1 to 80 bytes")
	}
	endpoint, err := Endpoint(in.Endpoint, p.PrivateNetworkID)
	if err != nil {
		return p, err
	}
	p.Endpoint = endpoint
	seen := map[string]bool{}
	for _, capability := range p.Capabilities {
		if !provider.ValidID(capability) || seen[capability] {
			return p, errors.New("choose distinct provider capabilities")
		}
		seen[capability] = true
	}
	if len(p.Capabilities) == 0 {
		p.Capabilities = []string{provider.CapabilityInspect}
	}
	if p.PrivateNetworkID != "" {
		network, err := m.Store.GetPrivateNetwork(ctx, p.PrivateNetworkID)
		if err != nil || network.Driver != edge.DriverAgent {
			return p, errors.New("choose an enrolled edge node for private provider access")
		}
	}
	if p.CredentialSecretID != "" {
		secret, err := m.Store.GetSecret(ctx, p.CredentialSecretID)
		if err != nil || core.PlainSecretType(secret.Type) || core.JSONSecretType(secret.Type) || secret.Type == core.SecretTypeSSHPrivateKey {
			return p, errors.New("choose a write-only text credential for provider authentication")
		}
	}
	return p, nil
}

func (m *Manager) Register(ctx context.Context, in Registration) (core.InfrastructureProvider, error) {
	p, err := m.input(ctx, in)
	if err != nil {
		return p, err
	}
	now := time.Now().UTC()
	p.ID = ulid.Make().String()
	p.Revision = 1
	p.State = "unverified"
	p.CreatedAt = now
	p.UpdatedAt = now
	if err = m.Store.CreateInfrastructureProvider(ctx, p); err != nil {
		return p, err
	}
	return m.Verify(ctx, p.ID, p.Revision)
}
func (m *Manager) Update(ctx context.Context, id string, in Registration) (core.InfrastructureProvider, error) {
	old, err := m.Store.GetInfrastructureProvider(ctx, id)
	if err != nil {
		return old, err
	}
	if in.Revision != old.Revision {
		return old, store.ErrProviderChanged
	}
	p, err := m.input(ctx, in)
	if err != nil {
		return old, err
	}
	if p.Endpoint != old.Endpoint || p.PrivateNetworkID != old.PrivateNetworkID {
		return old, errors.New("endpoint and route are fixed; register a new provider to change its target")
	}
	old.Name = p.Name
	old.CredentialSecretID = p.CredentialSecretID
	old.Enabled = p.Enabled
	old.Capabilities = p.Capabilities
	old.UpdatedAt = time.Now().UTC()
	old.Revision++
	old.State = "unverified"
	old.LastError = ""
	old.LastVerifiedAt = nil
	if !old.Enabled {
		old.State = "disabled"
	}
	if err = m.Store.UpdateInfrastructureProvider(ctx, old, in.Revision); err != nil {
		return old, err
	}
	if !old.Enabled {
		return old, nil
	}
	return m.Verify(ctx, id, old.Revision)
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (m *Manager) client(ctx context.Context, p core.InfrastructureProvider) (*provider.Client, string, error) {
	var token string
	if p.CredentialSecretID != "" {
		if m.Secrets == nil {
			return nil, "", errors.New("provider credential resolution is unavailable")
		}
		secret, err := m.Store.GetSecret(ctx, p.CredentialSecretID)
		if err != nil || core.PlainSecretType(secret.Type) || core.JSONSecretType(secret.Type) || secret.Type == core.SecretTypeSSHPrivateKey {
			return nil, "", errors.New("provider credential is unavailable or has an incompatible type")
		}
		raw, err := m.Secrets.Resolve(ctx, p.CredentialSecretID)
		if err != nil {
			return nil, "", errors.New("provider credential resolution failed")
		}
		token = string(raw)
		clear(raw)
		if token == "" || len(token) > 64<<10 {
			return nil, "", errors.New("provider credential is empty or exceeds its limit")
		}
	}
	client := m.HTTPClient
	if p.PrivateNetworkID != "" {
		network, err := m.Store.GetPrivateNetwork(ctx, p.PrivateNetworkID)
		if err != nil || network.Driver != edge.DriverAgent || m.Edge == nil {
			return nil, "", errors.New("private provider route is unavailable")
		}
		client = &http.Client{Timeout: 30 * time.Second, Transport: transportFunc(func(r *http.Request) (*http.Response, error) { return m.Edge.Do(r.Context(), network, r) })}
	}
	adapter, err := provider.NewClient(p.Endpoint, token, client)
	return adapter, token, err
}

func manifestDigest(manifest provider.Manifest) (json.RawMessage, string, error) {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}
func checkedManifest(ctx context.Context, client provider.Provider, token string) (provider.Manifest, json.RawMessage, string, error) {
	manifest, err := client.Manifest(ctx)
	if err != nil {
		if errors.Is(err, provider.ErrAPIVersion) {
			return manifest, nil, "", provider.ErrAPIVersion
		}
		var problem *provider.Problem
		if errors.Is(err, provider.ErrTransport) || errors.As(err, &problem) && problem.Status >= 500 {
			return manifest, nil, "", provider.ErrTransport
		}
		return manifest, nil, "", errors.New("provider manifest is invalid; restore the previous adapter and verify; supported versions: " + provider.APIVersion)
	}
	if _, _, err = configurationSchema(manifest.ConfigurationSchema); err != nil {
		return manifest, nil, "", err
	}
	raw, digest, err := manifestDigest(manifest)
	if err != nil || token != "" && bytes.Contains(raw, []byte(token)) {
		return manifest, nil, "", errors.New("provider manifest contains invalid or sensitive metadata")
	}
	return manifest, raw, digest, nil
}

func (m *Manager) Verify(ctx context.Context, id string, expected int64) (core.InfrastructureProvider, error) {
	p, err := m.Store.GetInfrastructureProvider(ctx, id)
	if err != nil {
		return p, err
	}
	if p.Revision != expected {
		return p, store.ErrProviderChanged
	}
	client, token, verifyErr := m.client(ctx, p)
	var manifest provider.Manifest
	var raw json.RawMessage
	var digest string
	if verifyErr == nil {
		manifest, raw, digest, verifyErr = checkedManifest(ctx, client, token)
	}
	if verifyErr == nil && len(p.Manifest) > 0 {
		var previous provider.Manifest
		if json.Unmarshal(p.Manifest, &previous) != nil || manifest.Name != previous.Name || manifest.APIVersion != previous.APIVersion {
			verifyErr = ErrIdentity
		}
	}
	if verifyErr == nil {
		for _, capability := range p.Capabilities {
			if !slices.Contains(manifest.Capabilities, capability) {
				verifyErr = errors.New("a selected capability is absent from the provider manifest")
				break
			}
		}
	}
	now := time.Now().UTC()
	p.Revision++
	p.UpdatedAt = now
	p.LastError = ""
	if verifyErr != nil {
		p.Enabled = false
		p.State = "failed"
		p.LastVerifiedAt = nil
		p.LastError = verifyErr.Error()
	} else {
		p.Manifest = raw
		p.ManifestDigest = digest
		p.LastVerifiedAt = &now
		p.State = "disabled"
		if p.Enabled {
			p.State = "ready"
		}
	}
	if err = m.Store.UpdateInfrastructureProvider(ctx, p, expected); err != nil {
		return p, err
	}
	return p, nil
}

// Adapter rechecks the live manifest before any caller may use the connection.
// Disabling denies new mutations, while inspection remains available.
func (m *Manager) Adapter(ctx context.Context, id, capability, digest string) (*provider.Client, core.InfrastructureProvider, error) {
	p, err := m.Store.GetInfrastructureProvider(ctx, id)
	if err != nil {
		return nil, p, err
	}
	mutation := capability == provider.CapabilityCreate || capability == provider.CapabilityDelete
	if mutation && (!p.Enabled || p.State != "ready") {
		return nil, p, ErrDisabled
	}
	if !slices.Contains(p.Capabilities, capability) {
		return nil, p, errors.New("provider capability is not approved")
	}
	if p.ManifestDigest == "" || digest != "" && digest != p.ManifestDigest {
		return nil, p, ErrIdentity
	}
	client, token, err := m.client(ctx, p)
	if err != nil {
		return nil, p, err
	}
	_, _, actual, err := checkedManifest(ctx, client, token)
	if err != nil {
		return nil, p, err
	}
	if actual != p.ManifestDigest {
		return nil, p, ErrIdentity
	}
	return client, p, nil
}

func (m *Manager) Options(ctx context.Context, id, kind string, config map[string]any) ([]provider.Option, error) {
	client, p, err := m.Adapter(ctx, id, provider.CapabilityInspect, "")
	if err != nil {
		return nil, err
	}
	var manifest provider.Manifest
	if json.Unmarshal(p.Manifest, &manifest) != nil {
		return nil, ErrIdentity
	}
	if err = ValidateConfiguration(manifest.ConfigurationSchema, config); err != nil {
		return nil, err
	}
	if err = client.Validate(ctx, config); err != nil {
		return nil, errors.New("provider rejected the supplied configuration")
	}
	items, err := client.Options(ctx, provider.OptionRequest{Kind: kind, Config: config})
	if err != nil {
		return nil, errors.New("provider option discovery failed")
	}
	return items, nil
}
