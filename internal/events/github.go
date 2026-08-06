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
)

type GitHubNotifier struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

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
	if n.Token != "" {
		request.Header.Set("Authorization", "Bearer "+n.Token)
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
		return "", fmt.Errorf("preview comment update returned %s", response.Status)
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
	BaseURL string
	Token   string
	Client  *http.Client
}

func (r GitHubResolver) ResolvePullRequest(ctx context.Context, repository string, number int) (SourceRevision, error) {
	owner, name, ok := strings.Cut(NormalizeRepository(repository), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return SourceRevision{}, fmt.Errorf("invalid repository %q", repository)
	}
	baseURL := strings.TrimRight(r.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", baseURL, url.PathEscape(owner), url.PathEscape(name), number)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return SourceRevision{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	if r.Token != "" {
		request.Header.Set("Authorization", "Bearer "+r.Token)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return SourceRevision{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return SourceRevision{}, fmt.Errorf("pull request lookup returned %s", response.Status)
	}
	var payload struct {
		State string `json:"state"`
		Head  struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return SourceRevision{}, err
	}
	if payload.Head.Ref == "" || payload.Head.SHA == "" {
		return SourceRevision{}, errors.New("pull request source revision is incomplete")
	}
	return SourceRevision{HeadRef: payload.Head.Ref, HeadSHA: payload.Head.SHA, BaseRef: payload.Base.Ref, Open: payload.State == "open"}, nil
}

func (r GitHubResolver) ResolveBranch(ctx context.Context, repository, branch string) (SourceRevision, error) {
	owner, name, ok := strings.Cut(NormalizeRepository(repository), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return SourceRevision{}, fmt.Errorf("invalid repository %q", repository)
	}
	baseURL := strings.TrimRight(r.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/commits/%s", baseURL, url.PathEscape(owner), url.PathEscape(name), url.PathEscape(branch))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return SourceRevision{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	if r.Token != "" {
		request.Header.Set("Authorization", "Bearer "+r.Token)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return SourceRevision{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return SourceRevision{}, fmt.Errorf("branch lookup returned %s", response.Status)
	}
	var payload struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return SourceRevision{}, err
	}
	if payload.SHA == "" {
		return SourceRevision{}, errors.New("branch revision is incomplete")
	}
	return SourceRevision{HeadRef: branch, HeadSHA: payload.SHA, Open: true}, nil
}
