package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
)

func TestPreviewPollStartsOnceAndClosesWithoutWebhooks(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "poll.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, create := range []func() error{
		func() error {
			return data.CreateProject(ctx, core.Project{ID: "project", Name: "Preview", CreatedAt: now})
		},
		func() error {
			return data.CreateServer(ctx, core.Server{ID: "server", Name: "Test", Runtime: core.ServerRuntimeDocker, State: "ready", CreatedAt: now})
		},
		func() error {
			return data.CreateApp(ctx, core.App{ID: "template", ProjectID: "project", ServerID: "server", Name: "Service", Template: true, BuildType: core.BuildTypeDockerfile, SourceRepo: "https://example.test/acme/service", Domain: "preview-{pr}.example.test", State: "ready", CreatedAt: now})
		},
	} {
		if err := create(); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err = data.CreateEventTrigger(ctx, core.EventTrigger{ID: "trigger", AppID: "template", Provider: core.EventProviderGitHub, Repository: "acme/service", Command: "/preview", Enabled: true, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	var closed atomic.Bool
	var comments atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues/comments"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 501, "body": "/preview", "issue_url": "https://example.test/repos/acme/service/issues/17", "author_association": "MEMBER", "created_at": now.Format(time.RFC3339), "user": map[string]string{"login": "operator"}}})
		case strings.HasSuffix(r.URL.Path, "/pulls/17"):
			state := "open"
			if closed.Load() {
				state = "closed"
			}
			_, _ = fmt.Fprintf(w, `{"state":%q,"head":{"ref":"feature","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"base":{"ref":"main"}}`, state)
		case strings.HasSuffix(r.URL.Path, "/issues/17/comments") && r.Method == http.MethodPost:
			comments.Add(1)
			_, _ = io.WriteString(w, `{"id":900}`)
		case strings.HasSuffix(r.URL.Path, "/issues/comments/900") && r.Method == http.MethodPatch:
			_, _ = io.WriteString(w, `{"id":900}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(github.Close)
	a := New(data, deploy.NewService(data, deploy.SimulationExecutor{Delay: time.Millisecond}), false, AuthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)), EventConfig{GitHubAPIURL: github.URL, GitHubToken: "token"})
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	previews, err := data.ListPreviewEnvironments(ctx, "")
	if err != nil || len(previews) != 1 {
		t.Fatalf("expected one preview: %v %#v", err, previews)
	}
	if previews[0].HeadSHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || previews[0].SourceCommentID != "501" || comments.Load() != 1 {
		t.Fatalf("unexpected preview identity or status comment: %#v, comments=%d", previews[0], comments.Load())
	}
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	previews, err = data.ListPreviewEnvironments(ctx, "")
	if err != nil || len(previews) != 1 || previews[0].ID == "" {
		t.Fatalf("repeated poll created another preview: %v %#v", err, previews)
	}
	closed.Store(true)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		previews, err = data.ListPreviewEnvironments(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		if previews[0].State == core.PreviewReady {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	previews, err = data.ListPreviewEnvironments(ctx, "")
	if err != nil || len(previews) != 1 || previews[0].State != core.PreviewClosed {
		t.Fatalf("closed pull request did not clean preview: %v %#v", err, previews)
	}
}
