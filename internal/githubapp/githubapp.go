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
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/repositoryproxy"
	"github.com/doout/dispatch/internal/sourceerror"
)

type Store interface {
	GetGitHubApp(context.Context, string) (core.GitHubAppConnection, error)
	GetPrivateNetwork(context.Context, string) (core.PrivateNetwork, error)
}

type cachedToken struct {
	value       string
	expiresAt   time.Time
	permissions map[string]string
}

type Manager struct {
	logOIDC logOIDCVerifier
	Store   Store
	Vault   *secretcrypto.Vault
	Client  *http.Client
	Edge    *edge.Broker
	Proxy   *repositoryproxy.Service

	mu           sync.Mutex
	tokens       map[string]cachedToken
	repositories map[string]repositoryInstallation
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
	ID                  int64           `json:"id"`
	Account             string          `json:"account"`
	Target              string          `json:"target"`
	WebURL              string          `json:"webUrl"`
	RepositorySelection string          `json:"repositorySelection"`
	Suspended           bool            `json:"suspended"`
	MissingPermissions  []PermissionGap `json:"missingPermissions,omitempty"`
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
	Archived      bool   `json:"archived"`
	Disabled      bool   `json:"disabled"`
	WebURL        string `json:"webUrl"`
}

type RepositoryFile struct {
	Path     string `json:"path"`
	Contents []byte `json:"-"`
}

type Verification struct {
	InstallationID                   int64           `json:"installationId,omitempty"`
	Slug                             string          `json:"slug"`
	ClientID                         string          `json:"clientId"`
	RegistrationOwner                string          `json:"registrationOwner"`
	RegistrationOwnerType            string          `json:"registrationOwnerType"`
	InstallationAccount              string          `json:"installationAccount"`
	InstallationURL                  string          `json:"installationUrl"`
	RepositorySelection              string          `json:"repositorySelection"`
	RepositoryCount                  int             `json:"repositoryCount"`
	PushSubscribed                   bool            `json:"pushSubscribed"`
	MissingAppPermissions            []PermissionGap `json:"missingAppPermissions"`
	MissingInstallationPermissions   []PermissionGap `json:"missingInstallationPermissions"`
	AppPermissionsAvailable          bool            `json:"appPermissionsAvailable"`
	InstallationPermissionsAvailable bool            `json:"installationPermissionsAvailable"`
	TokenPermissionsAvailable        bool            `json:"tokenPermissionsAvailable"`
	MissingTokenPermissions          []PermissionGap `json:"missingTokenPermissions"`
}

type PermissionGap struct {
	Name     string `json:"name"`
	Required string `json:"required"`
	Granted  string `json:"granted"`
}

var requiredRepositoryPermissions = map[string]string{
	"checks":        "write",
	"contents":      "read",
	"issues":        "write",
	"pull_requests": "write",
	"statuses":      "write",
}

func ManifestPermissions(readOnly bool) map[string]string {
	if readOnly {
		return map[string]string{"contents": "read", "metadata": "read"}
	}
	permissions := make(map[string]string, len(requiredRepositoryPermissions)+1)
	for name, level := range requiredRepositoryPermissions {
		permissions[name] = level
	}
	permissions["metadata"] = "read"
	// Check reporting works without Actions. Log collection additionally needs
	// this grant; existing Apps can approve it when they enable GitHub logs.
	permissions["actions"] = "write"
	return permissions
}

func missingPermissions(granted map[string]string, readOnly bool) []PermissionGap {
	names := []string{"checks", "contents", "issues", "pull_requests", "statuses"}
	if readOnly {
		names = []string{"contents"}
	}
	missing := []PermissionGap{}
	for _, name := range names {
		required, actual := requiredRepositoryPermissions[name], granted[name]
		if actual == required || actual == "write" && required == "read" {
			continue
		}
		if actual == "" {
			actual = "none"
		}
		missing = append(missing, PermissionGap{Name: name, Required: required, Granted: actual})
	}
	return missing
}

func New(store Store, vault *secretcrypto.Vault) *Manager {
	return &Manager{Store: store, Vault: vault, tokens: map[string]cachedToken{}}
}

func (m *Manager) Invalidate(id string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	for key := range m.tokens {
		if strings.HasPrefix(key, id+":") {
			delete(m.tokens, key)
		}
	}
	for key := range m.repositories {
		if strings.HasPrefix(key, id+":") {
			delete(m.repositories, key)
		}
	}
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
	if err != nil || api.Scheme != "https" || api.Hostname() == "" || api.User != nil || api.RawQuery != "" || api.ForceQuery || api.Fragment != "" {
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
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return "", err
	}
	return m.installationToken(ctx, id, connection.InstallationID)
}

func (m *Manager) installationToken(ctx context.Context, id string, installation int64) (string, error) {
	if installation < 1 {
		return "", errors.New("GitHub App is not linked to an installation")
	}
	key := tokenCacheKey(id, installation)
	m.mu.Lock()
	if cached := m.tokens[key]; cached.value != "" && time.Until(cached.expiresAt) > 5*time.Minute {
		m.mu.Unlock()
		return cached.value, nil
	}
	m.mu.Unlock()
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return "", err
	}
	jwt, err := m.appJWT(connection)
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("%s/app/installations/%d/access_tokens", strings.TrimRight(connection.APIURL, "/"), installation)
	var body interface{}
	if connection.ReadOnly {
		body = map[string]map[string]string{"permissions": {"contents": "read"}}
	}
	var response struct {
		Token       string            `json:"token"`
		ExpiresAt   time.Time         `json:"expires_at"`
		Permissions map[string]string `json:"permissions"`
	}
	if err := m.request(ctx, http.MethodPost, endpoint, jwt, body, &response, connection); err != nil {
		return "", fmt.Errorf("create GitHub App installation token: %w", err)
	}
	if response.Token == "" {
		return "", errors.New("GitHub returned an empty installation token")
	}
	m.mu.Lock()
	m.tokens[key] = cachedToken{value: response.Token, expiresAt: response.ExpiresAt, permissions: response.Permissions}
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
	items := []Installation{}
	for page := 1; ; page++ {
		var response []struct {
			ID                  int64             `json:"id"`
			Target              string            `json:"target_type"`
			HTMLURL             string            `json:"html_url"`
			RepositorySelection string            `json:"repository_selection"`
			SuspendedAt         *time.Time        `json:"suspended_at"`
			Permissions         map[string]string `json:"permissions"`
			Account             struct {
				Login string `json:"login"`
			} `json:"account"`
		}
		endpoint := fmt.Sprintf("%s/app/installations?per_page=100&page=%d", strings.TrimRight(connection.APIURL, "/"), page)
		if err := m.request(ctx, http.MethodGet, endpoint, jwt, nil, &response, connection); err != nil {
			return nil, err
		}
		for _, item := range response {
			installation := Installation{ID: item.ID, Account: item.Account.Login, Target: item.Target, WebURL: item.HTMLURL, RepositorySelection: item.RepositorySelection, Suspended: item.SuspendedAt != nil}
			if item.Permissions != nil {
				installation.MissingPermissions = missingPermissions(item.Permissions, connection.ReadOnly)
			}
			items = append(items, installation)
		}
		if len(response) < 100 {
			break
		}
	}

	return items, nil
}

func (m *Manager) ListRepositories(ctx context.Context, id string) ([]Repository, error) {
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return nil, err
	}
	installations, err := m.ListInstallations(ctx, id)
	if err != nil {
		return nil, err
	}
	items := []Repository{}
	for _, installation := range installations {
		if installation.Suspended {
			continue
		}
		token, err := m.installationToken(ctx, id, installation.ID)
		if err != nil {
			return nil, fmt.Errorf("installation %s: %w", installation.Account, err)
		}
		for page := 1; ; page++ {
			var response struct {
				Repositories []struct {
					ID            int64  `json:"id"`
					FullName      string `json:"full_name"`
					Name          string `json:"name"`
					DefaultBranch string `json:"default_branch"`
					Private       bool   `json:"private"`
					Archived      bool   `json:"archived"`
					Disabled      bool   `json:"disabled"`
					HTMLURL       string `json:"html_url"`
					Owner         struct {
						Login string `json:"login"`
					} `json:"owner"`
				} `json:"repositories"`
			}
			endpoint := fmt.Sprintf("%s/installation/repositories?per_page=100&page=%d", strings.TrimRight(connection.APIURL, "/"), page)
			if err := m.request(ctx, http.MethodGet, endpoint, token, nil, &response, connection); err != nil {
				return nil, fmt.Errorf("list installation repositories: %w", err)
			}
			for _, repository := range response.Repositories {
				items = append(items, Repository{ID: repository.ID, FullName: repository.FullName, Name: repository.Name,
					Owner: repository.Owner.Login, DefaultBranch: repository.DefaultBranch, Private: repository.Private, Archived: repository.Archived, Disabled: repository.Disabled, WebURL: repository.HTMLURL})
			}
			if len(response.Repositories) < 100 {
				break
			}
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
	token, err := m.RepositoryToken(ctx, id, repository)
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
	if err := m.request(ctx, http.MethodGet, endpoint, token, nil, &response, connection); err != nil {
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
	token, err := m.RepositoryToken(ctx, id, repository)
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
	if err := m.request(ctx, http.MethodGet, endpoint, token, nil, &tree, connection); err != nil {
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
		if err := m.request(ctx, http.MethodGet, endpoint, token, nil, &response, connection); err != nil {
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
	return m.SetCommitStatusWithContext(ctx, id, repository, revision, state, "Dispatch/deployment", description, targetURL)
}

func (m *Manager) SetCommitStatusWithContext(ctx context.Context, id, repository, revision, state, statusContext, description, targetURL string) error {
	if state != "pending" && state != "success" && state != "failure" && state != "error" {
		return errors.New("invalid commit status state")
	}
	if statusContext == "" || len(statusContext) > 100 || len(description) > 140 {
		return errors.New("invalid commit status context or description")
	}

	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return err
	}
	if connection.ReadOnly {
		return nil
	}
	token, err := m.RepositoryToken(ctx, id, repository)
	if err != nil {
		return err
	}
	repository, err = repositoryPath(repository)
	if err != nil {
		return err
	}
	payload := map[string]string{
		"state":       state,
		"context":     statusContext,
		"description": description,
	}
	if strings.TrimSpace(targetURL) != "" {
		payload["target_url"] = targetURL
	}
	endpoint := fmt.Sprintf("%s/repos/%s/statuses/%s", strings.TrimRight(connection.APIURL, "/"), repository, url.PathEscape(revision))
	if err := m.request(ctx, http.MethodPost, endpoint, token, payload, nil, connection); err != nil {
		return fmt.Errorf("publish commit status: %w", feedbackPermissionError(err, "Commit statuses"))
	}
	return nil
}

func repositoryPath(repository string) (string, error) {
	repository = strings.Trim(strings.TrimSpace(repository), "/")
	if !strings.Contains(repository, "://") && strings.Contains(repository, "@") {
		if _, value, ok := strings.Cut(repository, ":"); ok {
			repository = value
		}
	}
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
		Events      []string          `json:"events"`
		Permissions map[string]string `json:"permissions"`
	}
	if err := m.request(ctx, http.MethodGet, strings.TrimRight(connection.APIURL, "/")+"/app", jwt, nil, &app, connection); err != nil {
		return Verification{}, fmt.Errorf("verify GitHub App registration: %w", err)
	}
	if app.ID != connection.AppID {
		return Verification{}, fmt.Errorf("private key belongs to GitHub App %d, not %d", app.ID, connection.AppID)
	}
	result := Verification{Slug: app.Slug, ClientID: app.ClientID, RegistrationOwner: app.Owner.Login, RegistrationOwnerType: app.Owner.Type}
	result.PushSubscribed = slices.Contains(app.Events, "push")
	result.AppPermissionsAvailable = app.Permissions != nil
	if result.AppPermissionsAvailable {
		result.MissingAppPermissions = missingPermissions(app.Permissions, connection.ReadOnly)
	}
	installations, err := m.ListInstallations(ctx, id)
	if err != nil {
		return result, err
	}
	selected := int64(0)
	for _, installation := range installations {
		if installation.Suspended {
			continue
		}
		if selected == 0 || installation.ID == connection.InstallationID {
			selected = installation.ID
		}
		if installation.ID == connection.InstallationID {
			break
		}
	}
	connection.InstallationID = selected
	if selected == 0 {
		return result, nil
	}

	result.InstallationID = connection.InstallationID
	var installation struct {
		ID                  int64             `json:"id"`
		HTMLURL             string            `json:"html_url"`
		RepositorySelection string            `json:"repository_selection"`
		Permissions         map[string]string `json:"permissions"`
		Account             struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	endpoint := fmt.Sprintf("%s/app/installations/%d", strings.TrimRight(connection.APIURL, "/"), connection.InstallationID)
	if err := m.request(ctx, http.MethodGet, endpoint, jwt, nil, &installation, connection); err != nil {
		return Verification{}, fmt.Errorf("verify GitHub App installation: %w", err)
	}
	if installation.ID != connection.InstallationID {
		return Verification{}, errors.New("GitHub returned a different installation")
	}
	result.InstallationAccount = installation.Account.Login
	result.InstallationURL = installation.HTMLURL
	result.RepositorySelection = installation.RepositorySelection
	result.InstallationPermissionsAvailable = installation.Permissions != nil
	if result.InstallationPermissionsAvailable {
		result.MissingInstallationPermissions = missingPermissions(installation.Permissions, connection.ReadOnly)
	}
	token, err := m.installationToken(ctx, id, connection.InstallationID)
	if err != nil {
		return Verification{}, err
	}
	m.mu.Lock()
	tokenPermissions := m.tokens[tokenCacheKey(id, connection.InstallationID)].permissions
	m.mu.Unlock()
	result.TokenPermissionsAvailable = tokenPermissions != nil
	if result.TokenPermissionsAvailable {
		result.MissingTokenPermissions = missingPermissions(tokenPermissions, connection.ReadOnly)
	}
	var repositories struct {
		Total int `json:"total_count"`
	}
	if err := m.request(ctx, http.MethodGet, strings.TrimRight(connection.APIURL, "/")+"/installation/repositories?per_page=1", token, nil, &repositories, connection); err != nil {
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
	if err := m.request(ctx, http.MethodPatch, endpoint, jwt, payload, nil, connection); err != nil {
		return fmt.Errorf("configure GitHub App webhook: %w", err)
	}
	return nil
}

func (m *Manager) ConvertManifest(ctx context.Context, apiURL, code string, privateNetworkID ...string) (ManifestConversion, error) {
	connection := core.GitHubAppConnection{APIURL: apiURL}
	if len(privateNetworkID) > 0 {
		connection.PrivateNetworkID = privateNetworkID[0]
	}
	return m.ConvertAppManifest(ctx, connection, code)
}

func (m *Manager) ConvertAppManifest(ctx context.Context, connection core.GitHubAppConnection, code string) (ManifestConversion, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(connection.APIURL), "/") + "/app-manifests/" + url.PathEscape(strings.TrimSpace(code)) + "/conversions"
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
	if err := m.request(ctx, http.MethodPost, endpoint, "", nil, &result, connection); err != nil {
		return ManifestConversion{}, fmt.Errorf("GitHub App registration failed: %w", ConnectionDiagnostic(err))
	}
	if result.ID < 1 || result.PEM == "" {
		return ManifestConversion{}, errors.New("GitHub returned an incomplete App manifest conversion")
	}
	return ManifestConversion{AppID: result.ID, ClientID: result.ClientID, ClientSecret: result.ClientSecret, Name: result.Name, Slug: result.Slug,
		RegistrationOwner: result.Owner.Login, RegistrationOwnerType: result.Owner.Type,
		PrivateKey: result.PEM, WebhookSecret: result.WebhookSecret}, nil
}

// ConnectionDiagnostic describes a transport or HTTP failure without exposing
// request URLs, proxy credentials or upstream response bodies to the user.
func ConnectionDiagnostic(err error) error {
	var upstream *sourceerror.HTTPError
	if errors.As(err, &upstream) {
		return fmt.Errorf("GitHub returned HTTP %d.", upstream.StatusCode)
	}
	var requestError *url.Error
	if errors.As(err, &requestError) {
		err = requestError.Err
	}
	// Edge transports can relay error text as strings. Even the nested cause
	// may echo the URL or credentials, so only return recognized diagnostics.
	cause := strings.ToLower(err.Error())
	if strings.Contains(cause, "x509: certificate signed by unknown authority") {
		if strings.Contains(cause, "proxyconnect") {
			return errors.New("The controller does not trust the HTTPS proxy certificate (x509: certificate signed by unknown authority). Check the proxy certificate chain and the controller host’s trusted CAs.")
		}
		return errors.New("The GitHub API certificate was signed by an unknown authority (x509: certificate signed by unknown authority). Check the API certificate chain and any TLS-inspection CA on the controller or selected edge node.")
	}
	if strings.Contains(cause, "proxy authentication required") {
		return errors.New("The proxy rejected the connection (proxy authentication required). Check the proxy credentials.")
	}
	if strings.Contains(cause, "private integration addresses require an enrolled relay") {
		return errors.New("Select an enrolled edge node to reach a private GitHub API address.")
	}
	for _, diagnostic := range []string{
		"x509: certificate has expired or is not yet valid",
		"tls: first record does not look like a tls handshake",
		"tls handshake timeout",
		"tls: handshake failure",
		"tls: failed to verify certificate",
		"connection refused",
		"connection reset by peer",
		"no such host",
		"context deadline exceeded",
		"edge routing is not configured",
	} {
		if strings.Contains(cause, diagnostic) {
			return errors.New(diagnostic)
		}
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return errors.New("DNS lookup failed.")
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return errors.New("The connection timed out.")
	}
	return errors.New("The connection request failed.")
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

func (m *Manager) request(ctx context.Context, method, endpoint, token string, body interface{}, output interface{}, connection core.GitHubAppConnection) error {
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
	if method == http.MethodGet {
		request.Header.Set("Cache-Control", "no-cache")
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := m.routedClient(appTransport{connection: &connection}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return sourceerror.FromResponse(response)
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

type responseError = sourceerror.HTTPError
