package githubapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPullRequestHeadReadsActualBranch(t *testing.T) {
	const branch = "feature/current#tip"
	oldSHA, tip := strings.Repeat("a", 40), strings.Repeat("b", 40)
	state, failure, badRef := "open", false, false
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation"):
			id := 73
			if strings.Contains(r.URL.Path, "/contributor/") {
				id = 74
			}
			json.NewEncoder(w).Encode(map[string]int{"id": id, "app_id": 42})
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			token := "repository-token"
			if strings.Contains(r.URL.Path, "/74/") {
				token = "fork-token"
			}
			json.NewEncoder(w).Encode(map[string]any{"token": token, "expires_at": time.Now().Add(time.Hour)})
		case r.URL.Path == "/repos/owner/service/pulls/59":
			json.NewEncoder(w).Encode(map[string]any{"state": state, "head": map[string]any{"ref": branch, "sha": oldSHA, "repo": map[string]any{"id": 2, "full_name": "contributor/fork"}}, "base": map[string]any{"repo": map[string]any{"id": 1, "full_name": "owner/service"}}})
		case r.URL.Path == "/repos/contributor/fork/git/ref/heads/"+branch:
			reads++
			if r.URL.RawQuery != "" || r.Header.Get("Cache-Control") != "no-cache" || r.Header.Get("Authorization") != "Bearer fork-token" {
				t.Errorf("branch request lost auth, escaping or revalidation: %s %v", r.URL, r.Header)
			}
			if failure {
				http.Error(w, "temporarily unavailable", 503)
				return
			}
			ref := "refs/heads/" + branch
			if badRef {
				ref = "refs/tags/" + branch
			}
			json.NewEncoder(w).Encode(map[string]any{"ref": ref, "object": map[string]string{"type": "commit", "sha": tip}})
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m := installationTestManager(t, server.URL)
	for _, expected := range []string{tip, strings.Repeat("c", 40), oldSHA} {
		tip = expected // Includes a force-push back to an earlier commit.
		head, err := m.PullRequestHead(context.Background(), "app", "owner/service", 59)
		if err != nil || head.Head.SHA != expected || head.Head.Repo.ID != 2 || head.Base.Repo.ID != 1 || head.Head.Ref != branch {
			t.Fatalf("actual head or origin metadata lost: %+v %v", head, err)
		}
	}
	failure = true
	if _, err := m.PullRequestHead(context.Background(), "app", "owner/service", 59); err == nil {
		t.Fatal("branch failure silently reused the PR's stale commit")
	}
	failure, badRef = false, true
	if _, err := m.PullRequestHead(context.Background(), "app", "owner/service", 59); err == nil {
		t.Fatal("a tag was accepted as a branch")
	}
	state = "closed"
	before := reads
	head, err := m.PullRequestHead(context.Background(), "app", "owner/service", 59)
	if err != nil || head.Head.SHA != oldSHA || reads != before {
		t.Fatalf("closed PR needed its deleted branch: %+v %v", head, err)
	}
}
