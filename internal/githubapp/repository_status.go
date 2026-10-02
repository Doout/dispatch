package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

// CheckRepository uses a fresh installation inventory. Cached credentials do not
// substitute for current access, and a renamed repository is never auto-selected.
func (m *Manager) CheckRepository(ctx context.Context, appID, repository string, expectedID int64) (core.RepositoryStatus, error) {
	status := core.RepositoryStatus{State: "inaccessible", RepositoryID: expectedID, FullName: repository, CheckedAt: time.Now().UTC(), Detail: "The GitHub App cannot access this repository.", Recovery: "Restore installation access, then check again. GitHub does not distinguish a private repository from missing access."}
	if _, err := repositoryPath(repository); err != nil {
		return status, errors.New("repository must use owner/name")
	}
	items, err := m.ListRepositories(ctx, appID)
	if err != nil {
		status.State, status.Detail, status.Recovery = "unavailable", "Repository access could not be checked.", "Check the GitHub App connection and retry. The configured source has not changed."
		var response *responseError
		if errors.As(err, &response) && (response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 404) {
			status.State, status.Detail, status.Recovery = "inaccessible", "The GitHub App could not list its repositories.", "Check installation access and App permissions, then retry."
		}
		return status, nil
	}
	var selected *Repository
	for i := range items {
		item := &items[i]
		if expectedID > 0 && item.ID == expectedID || expectedID == 0 && strings.EqualFold(item.FullName, repository) {
			selected = item
			break
		}
	}
	if selected == nil {
		if expectedID > 0 {
			if data, ok := m.Store.(interface {
				RepositoryDeleted(context.Context, string, int64) (bool, error)
			}); ok {
				deleted, err := data.RepositoryDeleted(ctx, appID, expectedID)
				if err != nil {
					return status, err
				}
				if deleted {
					status.State, status.Detail, status.Recovery = "deleted", "GitHub reported deletion of the configured repository.", "Restore the repository in GitHub or deliberately select a replacement. Historical revisions remain unchanged."
					return status, nil
				}
			}
			for _, item := range items {
				if strings.EqualFold(item.FullName, repository) {
					status.State, status.Detail, status.Recovery = "identity_changed", "The configured name now belongs to a different repository.", "Select that repository explicitly to replace the source, or restore access to the original repository."
					break
				}
			}
		}
		return status, nil
	}
	status.RepositoryID, status.FullName = selected.ID, selected.FullName
	status.State, status.Detail, status.Recovery = "accessible", "Repository access confirmed.", ""
	if selected.ID < 1 {
		status.State, status.Detail, status.Recovery = "unavailable", "GitHub returned no stable repository identity.", "Check the GitHub connection and retry."
	} else if selected.Disabled {
		status.State, status.Detail, status.Recovery = "disabled", "GitHub has disabled this repository.", "Resolve the repository restriction in GitHub before syncing."
	} else if selected.Archived {
		status.State, status.Detail, status.Recovery = "archived", "This repository is archived.", "Unarchive it in GitHub or deliberately select another repository."
	} else if !strings.EqualFold(selected.FullName, repository) {
		status.State, status.Detail, status.Recovery = "renamed", "The repository moved to "+selected.FullName+".", "Review and apply the current name. The branch and historical revisions will be preserved."
	}
	return status, nil
}

func RequireAccessible(status core.RepositoryStatus) error {
	if status.State == "accessible" {
		return nil
	}
	return fmt.Errorf("%s %s", status.Detail, status.Recovery)
}

type Branch struct {
	Name      string `json:"name"`
	SHA       string `json:"sha"`
	Protected bool   `json:"protected"`
}

func (m *Manager) RepositoryBranches(ctx context.Context, appID, repository string, expectedID int64) ([]Branch, error) {
	status, err := m.CheckRepository(ctx, appID, repository, expectedID)
	if err != nil {
		return nil, err
	}
	if err = RequireAccessible(status); err != nil {
		return nil, err
	}
	connection, err := m.Store.GetGitHubApp(ctx, appID)
	if err != nil {
		return nil, err
	}
	token, err := m.RepositoryToken(ctx, appID, repository)
	if err != nil {
		return nil, errors.New("Repository access changed; check the GitHub App installation and retry")
	}
	path, err := repositoryPath(repository)
	if err != nil {
		return nil, err
	}
	items := []Branch{}
	for page := 1; page <= 10; page++ {
		var response []struct {
			Name      string `json:"name"`
			Protected bool   `json:"protected"`
			Commit    struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		endpoint := fmt.Sprintf("%s/repos/%s/branches?per_page=100&page=%d", strings.TrimRight(connection.APIURL, "/"), path, page)
		if err = m.request(ctx, http.MethodGet, endpoint, token, nil, &response, connection.PrivateNetworkID); err != nil {
			return nil, errors.New("Branches could not be loaded. Check repository contents permission and retry; the selected branch has not changed.")
		}
		for _, branch := range response {
			items = append(items, Branch{Name: branch.Name, SHA: branch.Commit.SHA, Protected: branch.Protected})
		}
		if len(response) < 100 {
			return items, nil
		}
	}
	return nil, errors.New("Repository has more than 1,000 branches. Enter the exact branch name and verify it during sync.")
}
