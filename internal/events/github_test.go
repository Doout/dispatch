package events

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLinkedRepositoriesUseTheirOwnInstallationCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		repository := parts[2] + "/" + parts[3]
		if r.Header.Get("Authorization") != "Bearer token-for-"+repository {
			t.Error("request used a token for another repository")
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/pulls/") {
			fmt.Fprintf(w, `{"state":"open","head":{"ref":"feature","sha":"abc","repo":{"full_name":%q}},"base":{"ref":"main"}}`, repository)
		} else if strings.Contains(r.URL.Path, "/git/ref/heads/") {
			fmt.Fprint(w, `{"ref":"refs/heads/feature","object":{"type":"commit","sha":"abc"}}`)
		} else {
			fmt.Fprint(w, `{"id":42}`)
		}
	}))
	defer server.Close()
	source := func(ctx context.Context, repository string) (string, error) { return "token-for-" + repository, nil }
	resolver := GitHubResolver{BaseURL: server.URL, RepositoryTokenSource: source}
	notifier := GitHubNotifier{BaseURL: server.URL, RepositoryTokenSource: source}
	for _, repository := range []string{"alpha/service", "beta/ui"} {
		if _, err := resolver.ResolvePullRequest(context.Background(), repository, 42); err != nil {
			t.Fatal(err)
		}
		if _, err := notifier.UpdateComment(context.Background(), repository, 42, "", "Preview ready"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPullRequestResolverUsesHeadRepositoryBranch(t *testing.T) {
	oldSHA, tip := strings.Repeat("a", 40), strings.Repeat("b", 40)
	state, branchStatus := "open", http.StatusOK
	branchReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/owner/service/pulls/59":
			fmt.Fprintf(w, `{"state":%q,"head":{"ref":"feature/branch#1","sha":%q,"repo":{"id":2,"full_name":"contributor/fork"}},"base":{"ref":"main","repo":{"id":1,"full_name":"owner/service"}}}`, state, oldSHA)
		case "/repos/contributor/fork/git/ref/heads/feature/branch#1":
			branchReads++
			if r.Header.Get("Authorization") != "Bearer token-contributor/fork" || r.Header.Get("Cache-Control") != "no-cache" || r.URL.RawQuery != "" {
				t.Errorf("wrong branch credentials or escaped ref: %v %s", r.Header, r.URL)
			}
			if branchStatus != http.StatusOK {
				w.WriteHeader(branchStatus)
				return
			}
			fmt.Fprintf(w, `{"ref":"refs/heads/feature/branch#1","object":{"type":"commit","sha":%q}}`, tip)
		default:
			t.Errorf("unexpected endpoint %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	resolver := GitHubResolver{BaseURL: server.URL, RepositoryTokenSource: func(_ context.Context, repo string) (string, error) { return "token-" + repo, nil }}
	for _, expected := range []string{tip, strings.Repeat("c", 40), oldSHA} {
		tip = expected
		pr, err := resolver.ResolvePullRequest(context.Background(), "owner/service", 59)
		if err != nil || pr.HeadSHA != expected || pr.HeadRepositoryID != 2 || pr.RepositoryID != 1 || !pr.Open {
			t.Fatalf("stale PR SHA or changed repository identities: %+v %v", pr, err)
		}
	}
	for _, status := range []int{404, 403, 503} {
		branchStatus = status
		if _, err := resolver.ResolvePullRequest(context.Background(), "owner/service", 59); err == nil || errors.Is(err, ErrPullRequestNotFound) {
			t.Fatalf("HTTP %d used stale data or treated a missing branch as a missing PR: %v", status, err)
		}
	}
	state = "closed"
	before := branchReads
	pr, err := resolver.ResolvePullRequest(context.Background(), "owner/service", 59)
	if err != nil || pr.Open || pr.HeadSHA != oldSHA || branchReads != before {
		t.Fatalf("closed PR tried to read its deleted branch: %+v %v", pr, err)
	}
}
