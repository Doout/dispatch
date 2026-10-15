package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/sourceerror"
)

type GitHubNotifier struct {
	BaseURL               string
	Token                 string
	TokenSource           func(context.Context) (string, error)
	RepositoryTokenSource func(context.Context, string) (string, error)
	Client                *http.Client
}

var ErrCommentForbidden = errors.New("GitHub App cannot write issue comments")

func (n GitHubNotifier) UpdatePreview(ctx context.Context, notification Notification) (string, error) {
	return n.UpdateComment(ctx, notification.Preview.Repository, notification.Preview.PullRequestNumber,
		notification.Preview.StatusCommentID, previewComment(notification))
}

func (n GitHubNotifier) UpdateComment(ctx context.Context, repository string, number int, commentID, body string) (string, error) {
	owner, repository, ok := strings.Cut(NormalizeRepository(repository), "/")
	if !ok || owner == "" || repository == "" || strings.Contains(repository, "/") {
		return "", errors.New("preview repository is invalid")
	}
	baseURL := strings.TrimRight(n.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	method := http.MethodPost
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", baseURL, url.PathEscape(owner), url.PathEscape(repository), number)
	if commentID != "" {
		method = http.MethodPatch
		endpoint = fmt.Sprintf("%s/repos/%s/%s/issues/comments/%s", baseURL, url.PathEscape(owner), url.PathEscape(repository), url.PathEscape(commentID))
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Content-Type", "application/json")
	token, err := githubRepositoryToken(ctx, owner+"/"+repository, n.Token, n.TokenSource, n.RepositoryTokenSource)
	if err != nil {
		return "", err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := n.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := sourceerror.FromResponse(response)
		if response.StatusCode == http.StatusForbidden && !failure.RateLimited {
			return "", fmt.Errorf("%w: %w", ErrCommentForbidden, failure)
		}
		return "", fmt.Errorf("preview comment update: %w", failure)
	}
	var result struct {
		ID json.Number `json:"id"`
	}
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return "", err
	}
	return result.ID.String(), nil
}

func previewComment(notification Notification) string {
	state := strings.ReplaceAll(string(notification.State), "_", " ")
	if state == "" {
		state = "unknown"
	}
	var body strings.Builder
	body.WriteString("<!-- dispatch-preview:")
	body.WriteString(notification.Preview.ID)
	body.WriteString(" -->\n### Preview\n\n")
	body.WriteString("**Status:** ")
	body.WriteString(strings.ToUpper(state[:1]) + state[1:])
	body.WriteString("\n")
	if notification.URL != "" {
		body.WriteString("**URL:** [Open preview](")
		body.WriteString(notification.URL)
		body.WriteString(")\n")
	}
	if notification.Message != "" {
		body.WriteString("\n")
		body.WriteString(notification.Message)
		body.WriteString("\n")
	}
	return body.String()
}

type GitHubResolver struct {
	BaseURL               string
	Token                 string
	TokenSource           func(context.Context) (string, error)
	RepositoryTokenSource func(context.Context, string) (string, error)
	Client                *http.Client
}

var ErrPullRequestNotFound = errors.New("pull request not found")

func (r GitHubResolver) ResolvePullRequest(ctx context.Context, repository string, number int) (SourceRevision, error) {
	var payload struct {
		State string `json:"state"`
		Head  struct {
			Ref  string `json:"ref"`
			Repo struct {
				ID       int64  `json:"id"`
				FullName string `json:"full_name"`
			} `json:"repo"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref  string `json:"ref"`
			Repo struct {
				ID       int64  `json:"id"`
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	}
	if err := r.get(ctx, repository, fmt.Sprintf("pulls/%d", number), &payload); err != nil {
		var response *sourceerror.HTTPError
		if errors.As(err, &response) && response.StatusCode == http.StatusNotFound {
			return SourceRevision{}, ErrPullRequestNotFound
		}
		return SourceRevision{}, err
	}
	if payload.Head.Ref == "" || payload.Head.SHA == "" {
		return SourceRevision{}, errors.New("pull request source revision is incomplete")
	}
	if payload.State == "open" {
		if payload.Head.Repo.FullName == "" {
			return SourceRevision{}, errors.New("pull request head repository is unavailable")
		}
		var reference githubapp.BranchReference
		if err := r.get(ctx, payload.Head.Repo.FullName, "git/ref/heads/"+url.PathEscape(payload.Head.Ref), &reference); err != nil {
			return SourceRevision{}, fmt.Errorf("resolve PR head branch %s: %w", payload.Head.Ref, err)
		}
		sha, err := reference.Commit(payload.Head.Ref)
		if err != nil {
			return SourceRevision{}, err
		}
		payload.Head.SHA = sha
	}
	return SourceRevision{RepositoryID: payload.Base.Repo.ID, Repository: payload.Base.Repo.FullName, HeadRepositoryID: payload.Head.Repo.ID, HeadRepository: payload.Head.Repo.FullName, HeadRef: payload.Head.Ref, HeadSHA: payload.Head.SHA, BaseRef: payload.Base.Ref, Open: payload.State == "open"}, nil
}

func (r GitHubResolver) ResolveBranch(ctx context.Context, repository, branch string) (SourceRevision, error) {
	var payload struct {
		SHA string `json:"sha"`
	}
	if err := r.get(ctx, repository, "commits/"+url.PathEscape(branch), &payload); err != nil {
		return SourceRevision{}, err
	}
	if payload.SHA == "" {
		return SourceRevision{}, errors.New("branch revision is incomplete")
	}
	return SourceRevision{HeadRef: branch, HeadSHA: payload.SHA, Open: true}, nil
}

func (r GitHubResolver) get(ctx context.Context, repository, path string, output any) error {
	owner, name, ok := strings.Cut(NormalizeRepository(repository), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("invalid repository %q", repository)
	}
	baseURL := strings.TrimRight(r.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/%s", baseURL, url.PathEscape(owner), url.PathEscape(name), path)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Cache-Control", "no-cache")
	token, err := githubRepositoryToken(ctx, repository, r.Token, r.TokenSource, r.RepositoryTokenSource)
	if err != nil {
		return err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return sourceerror.FromResponse(response)
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func githubToken(ctx context.Context, static string, source func(context.Context) (string, error)) (string, error) {
	if source != nil {
		return source(ctx)
	}
	return static, nil
}

func githubRepositoryToken(ctx context.Context, repository, static string, source func(context.Context) (string, error), repositorySource func(context.Context, string) (string, error)) (string, error) {
	if repositorySource != nil {
		return repositorySource(ctx, NormalizeRepository(repository))
	}
	return githubToken(ctx, static, source)
}
