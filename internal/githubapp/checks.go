package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

var ErrCheckRunNotFound = errors.New("GitHub check has not been found")
var checkCommitPattern = regexp.MustCompile(`^[a-fA-F0-9]{40}([a-fA-F0-9]{24})?$`)

type CheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	SHA        string `json:"head_sha"`
	ExternalID string `json:"external_id"`
	HTMLURL    string `json:"html_url"`
	App        struct {
		ID int64 `json:"id"`
	} `json:"app"`
}
type CheckRunUpdate struct {
	Name        string     `json:"name"`
	SHA         string     `json:"head_sha,omitempty"`
	ExternalID  string     `json:"external_id"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion,omitempty"`
	DetailsURL  string     `json:"details_url,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Output      struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	} `json:"output"`
}

// CheckRunError deliberately omits upstream bodies, URLs and credentials.
// CreateUncertain forbids another POST after response loss or server failure.
type CheckRunError struct {
	Message         string
	CreateUncertain bool
}

func (e *CheckRunError) Error() string { return e.Message }

func checkRunFailure(err error, creating bool) error {
	var response *responseError
	if errors.As(err, &response) {
		switch response.StatusCode {
		case 401, 403:
			return &CheckRunError{Message: "Grant Checks: write to the GitHub App, approve the installation permission update or reinstall it, then verify the connection. Reporting will retry."}
		case 404:
			return &CheckRunError{Message: "The captured repository or check is inaccessible to this GitHub App. Check its installation access; reporting will retry."}
		case 400, 422:
			return &CheckRunError{Message: "GitHub rejected the captured check identity or commit. Inspect the resolved source commit and App installation."}
		}
	}
	return &CheckRunError{Message: "GitHub check reporting is unavailable. Dispatch will retry reconciliation without changing the workflow outcome.", CreateUncertain: creating}
}

func (m *Manager) checkRunRequestFailure(r core.WorkflowCheckReport, err error, creating bool) error {
	var response *responseError
	if errors.As(err, &response) && (response.StatusCode == 401 || response.StatusCode == 403) {
		// A repaired App grant needs a newly issued installation token.
		m.Invalidate(r.GitHubAppID)
	}
	return checkRunFailure(err, creating)
}

func (m *Manager) checkRunAccess(ctx context.Context, r core.WorkflowCheckReport) (core.GitHubAppConnection, string, string, error) {
	connection, err := m.Store.GetGitHubApp(ctx, r.GitHubAppID)
	if err != nil {
		return connection, "", "", &CheckRunError{Message: "The captured GitHub App connection is unavailable."}
	}
	if connection.AppID != r.AppID || strings.TrimRight(connection.APIURL, "/") != r.APIURL {
		return connection, "", "", &CheckRunError{Message: "The GitHub App identity or API host changed after this run was accepted; its check will not be published to another App."}
	}
	if !checkCommitPattern.MatchString(r.CommitSHA) || r.ExternalID == "" || r.Name == "" || len(r.Name) > 200 || strings.ContainsAny(r.Name, "\r\n\x00") {
		return connection, "", "", &CheckRunError{Message: "The captured check identity is invalid."}
	}
	repository, err := repositoryPath(r.Repository)
	if err != nil {
		return connection, "", "", &CheckRunError{Message: "The captured check repository is invalid."}
	}
	token, err := m.RepositoryToken(ctx, r.GitHubAppID, repository)
	if err != nil {
		return connection, "", "", m.checkRunRequestFailure(r, err, false)
	}
	return connection, repository, token, nil
}

func checkRunMatches(run CheckRun, r core.WorkflowCheckReport) bool {
	return run.ID > 0 && run.App.ID == r.AppID && run.Name == r.Name && run.SHA == r.CommitSHA && run.ExternalID == r.ExternalID
}

// FindCheckRun uses the exact commit and App-owned external identity. A comment
// marker or a check from another App cannot satisfy the reporting receipt.
func (m *Manager) FindCheckRun(ctx context.Context, r core.WorkflowCheckReport) (CheckRun, error) {
	connection, repository, token, err := m.checkRunAccess(ctx, r)
	if err != nil {
		return CheckRun{}, err
	}
	base := fmt.Sprintf("%s/repos/%s", strings.TrimRight(connection.APIURL, "/"), repository)
	if r.CheckID > 0 {
		var found CheckRun
		if err := m.request(ctx, http.MethodGet, fmt.Sprintf("%s/check-runs/%d", base, r.CheckID), token, nil, &found, connection.PrivateNetworkID); err != nil {
			return found, m.checkRunRequestFailure(r, err, false)
		}
		if !checkRunMatches(found, r) {
			return CheckRun{}, &CheckRunError{Message: "The saved GitHub check no longer matches its accepted App, commit or context."}
		}
		return found, nil
	}
	for page := 1; page <= 100; page++ {
		var response struct {
			Runs []CheckRun `json:"check_runs"`
		}
		query := url.Values{"filter": {"all"}, "per_page": {"100"}, "page": {fmt.Sprint(page)}, "check_name": {r.Name}, "app_id": {fmt.Sprint(r.AppID)}}
		endpoint := fmt.Sprintf("%s/commits/%s/check-runs?%s", base, url.PathEscape(r.CommitSHA), query.Encode())
		if err := m.request(ctx, http.MethodGet, endpoint, token, nil, &response, connection.PrivateNetworkID); err != nil {
			return CheckRun{}, m.checkRunRequestFailure(r, err, false)
		}
		for _, run := range response.Runs {
			if checkRunMatches(run, r) {
				return run, nil
			}
		}
		if len(response.Runs) < 100 {
			return CheckRun{}, ErrCheckRunNotFound
		}
	}
	return CheckRun{}, &CheckRunError{Message: "GitHub check history exceeded the reconciliation limit. No duplicate check was created."}
}

func (m *Manager) PublishCheckRun(ctx context.Context, r core.WorkflowCheckReport, payload CheckRunUpdate, create bool) (CheckRun, error) {
	connection, repository, token, err := m.checkRunAccess(ctx, r)
	if err != nil {
		return CheckRun{}, err
	}
	payload.Name, payload.ExternalID = r.Name, r.ExternalID
	method, endpoint := http.MethodPatch, fmt.Sprintf("%s/repos/%s/check-runs/%d", strings.TrimRight(connection.APIURL, "/"), repository, r.CheckID)
	if create {
		method = http.MethodPost
		endpoint = fmt.Sprintf("%s/repos/%s/check-runs", strings.TrimRight(connection.APIURL, "/"), repository)
		payload.SHA = r.CommitSHA
	} else if r.CheckID < 1 {
		return CheckRun{}, &CheckRunError{Message: "A saved check ID is required before updating GitHub."}
	}
	var response CheckRun
	if err := m.request(ctx, method, endpoint, token, payload, &response, connection.PrivateNetworkID); err != nil {
		return response, m.checkRunRequestFailure(r, err, create)
	}
	if !checkRunMatches(response, r) {
		return CheckRun{}, &CheckRunError{Message: "GitHub returned a check with a different identity. Dispatch will reconcile the accepted check before continuing.", CreateUncertain: create}
	}
	return response, nil
}
