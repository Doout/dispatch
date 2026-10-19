package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/doout/dispatch/internal/core"
)

type LogWorkflowRun struct {
	ID     int64  `json:"id"`
	Title  string `json:"display_title"`
	Event  string `json:"event"`
	Path   string `json:"path"`
	Branch string `json:"head_branch"`
	URL    string `json:"html_url"`
}

const logWorkflowFile = "doodship-logs.yml"

var ErrLogWorkflowNotFound = errors.New("GitHub log workflow has not appeared")

// ResolveLogWorkflow selects the standard collector on the repository's default
// branch, independently of the application branch being built. The reporting
// queue saves this identity before dispatch so retries keep using the same ref.
func (m *Manager) ResolveLogWorkflow(ctx context.Context, r core.WorkflowCheckReport) (string, error) {
	connection, repository, token, err := m.checkRunAccess(ctx, r)
	if err != nil {
		return "", err
	}
	var result struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	}
	endpoint := strings.TrimRight(connection.APIURL, "/") + "/repos/" + repository
	if err := m.request(ctx, http.MethodGet, endpoint, token, nil, &result, connection.PrivateNetworkID); err != nil {
		return "", m.checkRunRequestFailure(r, err, false)
	}
	if !strings.EqualFold(result.FullName, repository) || result.DefaultBranch == "" || strings.TrimSpace(result.DefaultBranch) != result.DefaultBranch || strings.ContainsAny(result.DefaultBranch, "\r\n\x00") {
		return "", &CheckRunError{Message: "GitHub did not return the captured repository's default branch. Log reporting will retry."}
	}
	return result.DefaultBranch, nil
}

func (m *Manager) FindLogWorkflow(ctx context.Context, r core.WorkflowCheckReport) (LogWorkflowRun, error) {
	connection, repository, token, err := m.checkRunAccess(ctx, r)
	if err != nil {
		return LogWorkflowRun{}, err
	}
	if !r.GitHubLogs || r.LogRef == "" || r.LogRequestedAt == nil {
		return LogWorkflowRun{}, errors.New("GitHub log workflow has not been dispatched")
	}
	base := strings.TrimRight(connection.APIURL, "/") + "/repos/" + repository + "/actions"
	matches := func(run LogWorkflowRun) bool {
		path := ".github/workflows/" + logWorkflowFile
		return run.ID > 0 && run.Event == "workflow_dispatch" && run.Title == "Doodship "+r.ID && (run.Path == path || run.Path == path+"@"+r.LogRef || run.Path == path+"@refs/heads/"+r.LogRef) && run.Branch == r.LogRef
	}
	for page := 1; page <= 10; page++ {
		query := url.Values{"event": {"workflow_dispatch"}, "branch": {r.LogRef}, "per_page": {"100"}, "page": {fmt.Sprint(page)}}
		query.Set("created", r.LogRequestedAt.Add(-5*time.Minute).UTC().Format(time.RFC3339)+".."+r.LogRequestedAt.Add(5*time.Minute).UTC().Format(time.RFC3339))
		var result struct {
			Runs []LogWorkflowRun `json:"workflow_runs"`
		}
		endpoint := base + "/workflows/" + logWorkflowFile + "/runs?" + query.Encode()
		if err := m.request(ctx, http.MethodGet, endpoint, token, nil, &result, connection.PrivateNetworkID); err != nil {
			return LogWorkflowRun{}, err
		}
		for _, run := range result.Runs {
			if matches(run) {
				return run, nil
			}
		}
		if len(result.Runs) < 100 {
			return LogWorkflowRun{}, ErrLogWorkflowNotFound
		}
	}
	return LogWorkflowRun{}, errors.New("GitHub log workflow history exceeded the reconciliation limit")
}

func (m *Manager) DispatchLogWorkflow(ctx context.Context, r core.WorkflowCheckReport) error {
	connection, repository, token, err := m.checkRunAccess(ctx, r)
	if err != nil {
		return err
	}
	if !r.GitHubLogs || r.LogRef == "" {
		return errors.New("GitHub log workflow is not configured")
	}
	endpoint := strings.TrimRight(connection.APIURL, "/") + "/repos/" + repository + "/actions/workflows/" + logWorkflowFile + "/dispatches"
	if err := m.request(ctx, http.MethodPost, endpoint, token, map[string]any{"ref": r.LogRef, "inputs": map[string]string{"report_id": r.ID}}, nil, connection.PrivateNetworkID); err != nil {
		uncertain := true
		var response *responseError
		if errors.As(err, &response) && response.StatusCode >= 400 && response.StatusCode < 500 && !response.RateLimited {
			uncertain = false
			if response.StatusCode == 401 || response.StatusCode == 403 {
				m.Invalidate(r.GitHubAppID)
			}
		}
		return &CheckRunError{Message: "GitHub could not start the log workflow. Check the workflow file, branch and Actions: write permission.", CreateUncertain: uncertain}
	}
	return nil
}

type logOIDCVerifier struct {
	mu   sync.Mutex
	keys map[string]*oidc.Provider
}

func (m *Manager) VerifyLogWorkflowToken(ctx context.Context, r core.WorkflowCheckReport, raw string) error {
	if !r.GitHubLogs || r.LogRunID < 1 || len(raw) > 16384 || len(raw) < 128 || strings.Count(raw, ".") != 2 {
		return errors.New("GitHub workflow identity is required")
	}
	connection, err := m.Store.GetGitHubApp(ctx, r.GitHubAppID)
	if err != nil || connection.AppID != r.AppID || strings.TrimRight(connection.APIURL, "/") != r.APIURL {
		return errors.New("GitHub App identity changed")
	}
	u, err := url.Parse(r.APIURL)
	if err != nil || u.Scheme != "https" {
		return errors.New("GitHub OIDC requires HTTPS")
	}
	issuer := "https://token.actions.githubusercontent.com"
	if !strings.EqualFold(u.Host, "api.github.com") {
		issuer = "https://" + u.Host + "/_services/token"
	}
	key := issuer + "\x00" + connection.PrivateNetworkID
	m.logOIDC.mu.Lock()
	if m.logOIDC.keys == nil {
		m.logOIDC.keys = map[string]*oidc.Provider{}
	}
	provider := m.logOIDC.keys[key]
	m.logOIDC.mu.Unlock()
	if provider == nil {
		client := &http.Client{Timeout: 15 * time.Second, Transport: logOIDCTransport{manager: m, network: connection.PrivateNetworkID, host: strings.TrimPrefix(issuer, "https://")}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		// The key cache must outlive the first incoming request's cancellation.
		clientContext := oidc.ClientContext(context.Background(), client)
		discovery, cancel := context.WithTimeout(clientContext, 15*time.Second)
		provider, err = oidc.NewProvider(discovery, issuer)
		cancel()
		if err != nil {
			return errors.New("GitHub workflow identity provider is unavailable")
		}
		m.logOIDC.mu.Lock()
		m.logOIDC.keys[key] = provider
		m.logOIDC.mu.Unlock()
	}
	verified, err := provider.Verifier(&oidc.Config{ClientID: "doodship:check:" + r.ID, SupportedSigningAlgs: []string{oidc.RS256}}).Verify(ctx, raw)
	if err != nil {
		return errors.New("GitHub workflow identity is invalid or expired")
	}
	var claims struct {
		Repository string `json:"repository"`
		Workflow   string `json:"workflow_ref"`
		Event      string `json:"event_name"`
		RunID      string `json:"run_id"`
		IssuedAt   int64  `json:"iat"`
	}
	if err := verified.Claims(&claims); err != nil {
		return errors.New("GitHub workflow identity is invalid")
	}
	expected := claims.Repository + "/.github/workflows/" + logWorkflowFile + "@refs/heads/" + r.LogRef
	if !strings.EqualFold(claims.Repository, r.Repository) || claims.Workflow != expected || claims.Event != "workflow_dispatch" || claims.RunID != fmt.Sprint(r.LogRunID) || claims.IssuedAt < 1 || time.Unix(claims.IssuedAt, 0).After(time.Now().Add(time.Minute)) || verified.Expiry.Sub(time.Unix(claims.IssuedAt, 0)) > 10*time.Minute {
		return errors.New("GitHub workflow identity does not match this run")
	}
	return nil
}

type logOIDCTransport struct {
	manager       *Manager
	network, host string
}

func (t logOIDCTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u, _ := url.Parse("https://" + t.host)
	if r.URL.Scheme != "https" || r.URL.Host != u.Host {
		return nil, errors.New("GitHub identity discovery changed host")
	}
	if t.network != "" {
		if t.manager.Edge == nil {
			return nil, errors.New("GitHub network route is unavailable")
		}
		network, err := t.manager.Store.GetPrivateNetwork(r.Context(), t.network)
		if err != nil {
			return nil, err
		}
		return t.manager.Edge.Do(r.Context(), network, r)
	}
	client := t.manager.Client
	if client != nil && client.Transport != nil {
		return client.Transport.RoundTrip(r)
	}
	return http.DefaultTransport.RoundTrip(r)
}
