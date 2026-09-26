package events

import (
	"context"
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
			fmt.Fprint(w, `{"state":"open","head":{"ref":"feature","sha":"abc"},"base":{"ref":"main"}}`)
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
