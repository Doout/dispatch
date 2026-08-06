package events

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestGitHubNotifierCreatesAndUpdatesStatusComment(t *testing.T) {
	methods := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "Open preview") || r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("unexpected notification request: headers=%v body=%s", r.Header, body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1234}`))
	}))
	defer server.Close()
	notifier := GitHubNotifier{BaseURL: server.URL, Token: "token", Client: server.Client()}
	preview := core.PreviewEnvironment{ID: "preview-1", Repository: "owner/app", PullRequestNumber: 12}
	id, err := notifier.UpdatePreview(context.Background(), Notification{Preview: preview, State: core.PreviewReady, URL: "https://preview.example.com"})
	if err != nil || id != "1234" {
		t.Fatalf("unexpected create result: id=%q err=%v", id, err)
	}
	preview.StatusCommentID = id
	if _, err := notifier.UpdatePreview(context.Background(), Notification{Preview: preview, State: core.PreviewClosed, URL: "https://preview.example.com"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "POST,PATCH" {
		t.Fatalf("unexpected notification methods: %v", methods)
	}
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"action":"created"}`)
	mac := hmac.New(sha256.New, []byte("webhook-secret"))
	_, _ = mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if err := VerifySignature("webhook-secret", body, signature); err != nil {
		t.Fatal(err)
	}
	if err := VerifySignature("webhook-secret", body, "sha256=00"); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected invalid signature, got %v", err)
	}
}

func TestParsePullRequestCommentCommand(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte(`{
      "action":"created",
      "repository":{"full_name":"Acme/Checkout"},
      "issue":{"number":42,"pull_request":{"url":"https://example.test/pulls/42"}},
	      "comment":{"id":987,"body":"/preview image.tag=abc\nignore this","author_association":"MEMBER","user":{"login":"octo"}}
    }`)
	event, err := ParseGitHubEvent("issue_comment", "delivery-1", payload, now)
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != core.EventKindPullRequestComment || event.Repository != "acme/checkout" || event.PullRequestNumber != 42 {
		t.Fatalf("unexpected normalized event: %#v", event)
	}
	if event.Command != "/preview" || event.Arguments != "image.tag=abc" || event.SourceCommentID != "987" {
		t.Fatalf("unexpected command data: %#v", event)
	}
	if !event.TrustedActor || event.ActorAssociation != "MEMBER" {
		t.Fatalf("expected member comment to be trusted: %#v", event)
	}
}

func TestParsePullRequestCommentRejectsUntrustedAssociation(t *testing.T) {
	payload := []byte(`{
	  "action":"created",
	  "repository":{"full_name":"acme/checkout"},
	  "issue":{"number":42,"pull_request":{"url":"https://example.test/pulls/42"}},
	  "comment":{"id":988,"body":"/preview","author_association":"CONTRIBUTOR","user":{"login":"outside"}}
	}`)
	event, err := ParseGitHubEvent("issue_comment", "delivery-untrusted", payload, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if event.TrustedActor {
		t.Fatalf("contributor command must not be trusted: %#v", event)
	}
}

func TestParsePullRequestCloseIncludesRevision(t *testing.T) {
	payload := []byte(`{
      "action":"closed",
      "repository":{"full_name":"acme/checkout"},
      "sender":{"login":"octo"},
      "pull_request":{"number":42,"head":{"ref":"feature/cart","sha":"abc123"},"base":{"ref":"main"}}
    }`)
	event, err := ParseGitHubEvent("pull_request", "delivery-2", payload, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if event.Action != "closed" || event.HeadRef != "feature/cart" || event.HeadSHA != "abc123" || event.BaseRef != "main" {
		t.Fatalf("unexpected close event: %#v", event)
	}
}
