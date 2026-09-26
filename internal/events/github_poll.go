package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHubComment is the part of a repository issue comment needed to recreate
// an issue_comment delivery. Issue comments include pull request comments.
type GitHubComment struct {
	ID                json.Number `json:"id"`
	Body              string      `json:"body"`
	IssueURL          string      `json:"issue_url"`
	AuthorAssociation string      `json:"author_association"`
	CreatedAt         time.Time   `json:"created_at"`
	User              struct {
		Login string `json:"login"`
	} `json:"user"`
}

type GitHubCommentReader struct {
	BaseURL     string
	Token       string
	TokenSource func(context.Context) (string, error)
	Client      *http.Client
}

func (r GitHubCommentReader) get(ctx context.Context, endpoint string, target any) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	token, err := githubToken(ctx, r.Token, r.TokenSource)
	if err != nil {
		return 0, err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return response.StatusCode, fmt.Errorf("GitHub comment poll returned %s", response.Status)
	}
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	return response.StatusCode, decoder.Decode(target)
}

func (r GitHubCommentReader) endpoint(repository string) (string, error) {
	owner, name, ok := strings.Cut(NormalizeRepository(repository), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("invalid repository %q", repository)
	}
	base := strings.TrimRight(r.BaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	return fmt.Sprintf("%s/repos/%s/%s", base, url.PathEscape(owner), url.PathEscape(name)), nil
}

func (r GitHubCommentReader) ListIssueComments(ctx context.Context, repository string, since time.Time) ([]GitHubComment, error) {
	endpoint, err := r.endpoint(repository)
	if err != nil {
		return nil, err
	}
	items := []GitHubComment{}
	for page := 1; page <= 50; page++ {
		query := fmt.Sprintf("?since=%s&per_page=100&page=%d", url.QueryEscape(since.UTC().Format(time.RFC3339)), page)
		var batch []GitHubComment
		if _, err := r.get(ctx, endpoint+"/issues/comments"+query, &batch); err != nil {
			return nil, err
		}
		items = append(items, batch...)
		if len(batch) < 100 {
			return items, nil
		}
	}
	return nil, errors.New("GitHub comment poll exceeded 5000 comments; cursor was not advanced")
}

func (c GitHubComment) IssueNumber(repository string) (int, error) {
	parsed, err := url.Parse(c.IssueURL)
	if err != nil {
		return 0, err
	}
	path := strings.Trim(parsed.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 5 || parts[len(parts)-2] != "issues" ||
		NormalizeRepository(parts[len(parts)-4]+"/"+parts[len(parts)-3]) != NormalizeRepository(repository) {
		return 0, errors.New("comment issue URL does not match repository")
	}
	number, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || number < 1 {
		return 0, errors.New("comment issue URL has no issue number")
	}
	return number, nil
}
