package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// RepositoryBranchHead reads the exact branch reference, never a same-named tag
// or GitHub's cached pull request head or merge reference.
func (m *Manager) RepositoryBranchHead(ctx context.Context, id, repository, branch string) (string, error) {
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return "", err
	}
	repo, err := repositoryPath(repository)
	if err != nil {
		return "", err
	}
	if branch == "" {
		return "", errors.New("branch name is required")
	}
	token, err := m.RepositoryToken(ctx, id, repository)
	if err != nil {
		return "", err
	}
	var reference BranchReference
	endpoint := fmt.Sprintf("%s/repos/%s/git/ref/heads/%s", strings.TrimRight(connection.APIURL, "/"), repo, url.PathEscape(branch))
	if err := m.request(ctx, http.MethodGet, endpoint, token, nil, &reference, connection.PrivateNetworkID); err != nil {
		return "", fmt.Errorf("resolve %s branch %s: %w", repository, branch, err)
	}
	return reference.Commit(branch)
}

// BranchReference is the response from GitHub's exact Git reference endpoint.
type BranchReference struct {
	Ref    string `json:"ref"`
	Object struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"object"`
}

func (r BranchReference) Commit(branch string) (string, error) {
	if r.Ref != "refs/heads/"+branch || r.Object.Type != "commit" || strings.TrimSpace(r.Object.SHA) == "" {
		return "", errors.New("GitHub returned an invalid branch reference")
	}
	return r.Object.SHA, nil
}
