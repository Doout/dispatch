package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

var ErrReviewOutdated = errors.New("PR closed, draft, or updated since the tested deployment")
var ErrReviewDismissed = errors.New("This QA review was dismissed. Post a new test command to request another decision")

type PullRequestHead struct {
	State string `json:"state"`
	Draft bool   `json:"draft"`
	Head  struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

func (m *Manager) PullRequestHead(ctx context.Context, id, repository string, number int) (PullRequestHead, error) {
	var head PullRequestHead
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return head, err
	}
	repo, err := repositoryPath(repository)
	if err != nil {
		return head, err
	}
	if number < 1 {
		return head, errors.New("invalid pull request number")
	}
	token, err := m.RepositoryToken(ctx, id, repo)
	if err != nil {
		return head, err
	}
	endpoint := fmt.Sprintf("%s/repos/%s/pulls/%d", strings.TrimRight(connection.APIURL, "/"), repo, number)
	err = m.request(ctx, http.MethodGet, endpoint, token, nil, &head, connection.PrivateNetworkID)
	return head, err
}

// SubmitPullRequestReview pins the review to the tested commit and recovers a
// previous successful POST if a controller stopped before saving its review ID.
func (m *Manager) SubmitPullRequestReview(ctx context.Context, id, repository string, number int, sha, event, marker, body string) (int64, error) {
	if event != "APPROVE" && event != "REQUEST_CHANGES" {
		return 0, errors.New("invalid pull request review decision")
	}
	if sha == "" || marker == "" {
		return 0, errors.New("review needs a tested commit and result marker")
	}
	connection, err := m.Store.GetGitHubApp(ctx, id)
	if err != nil {
		return 0, err
	}
	if connection.Slug == "" {
		return 0, errors.New("Verify the GitHub App connection to load its bot identity before enabling PR reviews")
	}
	repo, err := repositoryPath(repository)
	if err != nil {
		return 0, err
	}
	token, err := m.RepositoryToken(ctx, id, repo)
	if err != nil {
		return 0, err
	}
	endpoint := fmt.Sprintf("%s/repos/%s/pulls/%d/reviews", strings.TrimRight(connection.APIURL, "/"), repo, number)
	// The marker alone is not trusted. Only this App's bot can satisfy a retry.
	for page := 1; ; page++ {
		var reviews []struct {
			ID       int64  `json:"id"`
			Body     string `json:"body"`
			CommitID string `json:"commit_id"`
			State    string `json:"state"`
			User     struct {
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"user"`
		}
		if err := m.request(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d", endpoint, page), token, nil, &reviews, connection.PrivateNetworkID); err != nil {
			return 0, feedbackPermissionError(err, "Pull requests")
		}
		state := "APPROVED"
		if event == "REQUEST_CHANGES" {
			state = "CHANGES_REQUESTED"
		}
		for _, review := range reviews {
			if connection.Slug != "" && review.User.Type == "Bot" && strings.EqualFold(review.User.Login, connection.Slug+"[bot]") && review.CommitID == sha && strings.Contains(review.Body, marker) {
				if review.State == "DISMISSED" {
					return review.ID, ErrReviewDismissed
				}
				if review.State == state {
					return review.ID, nil
				}
			}
		}
		if len(reviews) < 100 {
			break
		}
	}
	// Check again immediately before submitting. commit_id also prevents an old
	// result from silently being attached to the newest PR commit.
	head, err := m.PullRequestHead(ctx, id, repo, number)
	if err != nil {
		return 0, err
	}
	if head.State != "open" || head.Draft || head.Head.SHA != sha {
		return 0, ErrReviewOutdated
	}
	var review struct {
		ID int64 `json:"id"`
	}
	payload := map[string]string{"commit_id": sha, "event": event, "body": marker + "\n" + body}
	if err := m.request(ctx, http.MethodPost, endpoint, token, payload, &review, connection.PrivateNetworkID); err != nil {
		return 0, feedbackPermissionError(err, "Pull requests")
	}
	if review.ID < 1 {
		return 0, errors.New("GitHub returned a review without an ID")
	}
	return review.ID, nil
}

func feedbackPermissionError(err error, permission string) error {
	var response *responseError
	if errors.As(err, &response) && response.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w. Grant %s: write to the GitHub App and accept the installation permission update for this repository", err, permission)
	}
	return err
}
