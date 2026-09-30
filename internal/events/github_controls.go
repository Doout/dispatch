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

type GitHubPullRequest struct {
	Number    int       `json:"number"`
	CreatedAt time.Time `json:"created_at"`
}

func (r GitHubCommentReader) ListOpenPullRequests(ctx context.Context, repository string) ([]GitHubPullRequest, error) {
	endpoint, err := r.endpoint(repository)
	if err != nil {
		return nil, err
	}
	items := []GitHubPullRequest{}
	for page := 1; page <= 50; page++ {
		var batch []GitHubPullRequest
		if _, err := r.get(ctx, fmt.Sprintf("%s/pulls?state=open&sort=created&direction=desc&per_page=100&page=%d", endpoint, page), &batch); err != nil {
			return nil, err
		}
		items = append(items, batch...)
		if len(batch) < 100 {
			return items, nil
		}
	}
	return nil, errors.New("GitHub PR discovery exceeded 5000 open pull requests")
}

func (r GitHubCommentReader) GetComment(ctx context.Context, repository, id string) (GitHubComment, error) {
	endpoint, err := r.endpoint(repository)
	if err != nil {
		return GitHubComment{}, err
	}
	var c GitHubComment
	_, err = r.get(ctx, endpoint+"/issues/comments/"+url.PathEscape(id), &c)
	return c, err
}

// REST reports the comment's author, even when another person edited it.
// Verify the exact body and editor with GraphQL, then check their current access.
func (r GitHubCommentReader) AuthorizedCommentEditor(ctx context.Context, repository string, c GitHubComment) (string, error) {
	if c.NodeID == "" {
		return "", errors.New("comment has no GraphQL node ID")
	}
	base := strings.TrimRight(r.BaseURL, "/")
	if base == "" || base == "https://api.github.com" {
		base = "https://api.github.com/graphql"
	} else {
		base = strings.TrimSuffix(base, "/v3") + "/graphql"
	}
	payload, err := json.Marshal(map[string]any{"query": `query($id: ID!) { node(id: $id) { ... on IssueComment { body updatedAt editor { login __typename } viewerDidAuthor } } }`, "variables": map[string]string{"id": c.NodeID}})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	token, err := githubToken(ctx, r.Token, r.TokenSource)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("comment editor lookup returned %s", resp.Status)
	}
	var result struct {
		Data struct {
			Node struct {
				Body            string
				UpdatedAt       time.Time
				ViewerDidAuthor bool
				Editor          *struct {
					Login string
					Type  string `json:"__typename"`
				}
			}
		}
		Errors []struct{ Message string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Errors) > 0 {
		return "", errors.New("GitHub could not verify the comment editor")
	}
	node := result.Data.Node
	if node.Body != c.Body || !node.UpdatedAt.Equal(c.UpdatedAt) {
		return "", errors.New("comment changed during editor verification; retrying")
	}
	if !node.ViewerDidAuthor || node.Editor == nil || node.Editor.Type != "User" {
		return "", nil
	}
	endpoint, err := r.endpoint(repository)
	if err != nil {
		return "", err
	}
	var permission struct{ Permission string }
	status, err := r.get(ctx, endpoint+"/collaborators/"+url.PathEscape(node.Editor.Login)+"/permission", &permission)
	if status == http.StatusNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if permission.Permission != "admin" && permission.Permission != "write" && permission.Permission != "maintain" {
		return "", nil
	}
	return node.Editor.Login, nil
}
