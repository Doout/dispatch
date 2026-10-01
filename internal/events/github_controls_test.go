package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckboxEditorMustOwnExactEditAndHaveWriteAccess(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name, permission, editorType string
		author, mismatch             bool
		want                         string
		wantErr                      bool
	}{
		{name: "writer", permission: "write", editorType: "User", author: true, want: "maintainer"},
		{name: "reader", permission: "read", editorType: "User", author: true},
		{name: "bot", permission: "admin", editorType: "Bot", author: true},
		{name: "spoofed panel", permission: "admin", editorType: "User"},
		{name: "changed during lookup", permission: "admin", editorType: "User", author: true, mismatch: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := GitHubComment{NodeID: "node", Body: "panel", UpdatedAt: now}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer installation" {
					t.Error("missing installation token")
				}
				if r.URL.Path == "/api/graphql" {
					body := c.Body
					if tc.mismatch {
						body = "a newer edit"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"node": map[string]any{"body": body, "updatedAt": now, "viewerDidAuthor": tc.author, "editor": map[string]string{"login": "maintainer", "__typename": tc.editorType}}}})
					return
				}
				if r.URL.Path != "/api/v3/repos/org/repo/collaborators/maintainer/permission" {
					t.Errorf("unexpected lookup: %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"permission": tc.permission})
			}))
			defer server.Close()
			editor, err := (GitHubCommentReader{BaseURL: server.URL + "/api/v3", Token: "installation"}).AuthorizedCommentEditor(context.Background(), "org/repo", c)
			if editor != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("editor=%q err=%v", editor, err)
			}
		})
	}
}

func TestOpenPRDiscoveryUsesRepositoryPagination(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		if r.URL.Path != "/repos/org/repo/pulls" || r.URL.Query().Get("state") != "open" {
			t.Errorf("unexpected URL %s", r.URL)
		}
		if pages == 1 {
			items := make([]GitHubPullRequest, 100)
			for i := range items {
				items[i].Number = i + 1
			}
			_ = json.NewEncoder(w).Encode(items)
		} else {
			_ = json.NewEncoder(w).Encode([]GitHubPullRequest{{Number: 101}})
		}
	}))
	defer server.Close()
	items, err := (GitHubCommentReader{BaseURL: server.URL}).ListOpenPullRequests(context.Background(), "org/repo")
	if err != nil || len(items) != 101 || pages != 2 {
		t.Fatalf("items=%d pages=%d err=%v", len(items), pages, err)
	}
}
