package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/deploy"
)

func webhookTestVault(t *testing.T) *secretcrypto.Vault {
	t.Helper()
	path := filepath.Join(t.TempDir(), "master-key")
	if err := os.WriteFile(path, []byte(strings.Repeat("!", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := secretcrypto.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

const webhookCommentBody = `{"action":"created","repository":{"full_name":"acme/service"},"issue":{"number":42,"pull_request":{}},"comment":{"id":123,"body":"/preview password=do-not-expose","author_association":"NONE","user":{"login":"visitor"}}}`

func TestWebhookAcknowledgesDurableReceiptBeforeWorkAndResumesAfterRestart(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	secret := "webhook-secret"
	send := func(api *API, event, id, body string) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		api.processGitHubWebhook(rr, signedWebhookRequest(secret, event, id, []byte(body)), secret, "", 0, api.groups, api.events)
		return rr
	}
	rr := send(a, "issue_comment", "delivery-1", webhookCommentBody)
	if rr.Code != 202 {
		t.Fatalf("accept: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "do-not-expose") || strings.Contains(rr.Body.String(), "ciphertext") {
		t.Fatal("receipt leaked private payload")
	}
	exists, err := a.store.IncomingEventExists(ctx, core.EventProviderGitHub, "comment::acme/service:123")
	if err != nil || exists {
		t.Fatalf("work happened before acknowledgement: %t %v", exists, err)
	}
	items, err := a.store.ListWebhookDeliveries(ctx, "", 50)
	if err != nil || len(items) != 1 || items[0].State != "queued" || items[0].Ciphertext == "" || strings.Contains(items[0].Ciphertext, "do-not-expose") {
		t.Fatalf("durable encrypted receipt: %+v %v", items, err)
	}
	// A fresh API/controller instance consumes the saved body without a resend.
	restarted := New(a.store, deploy.NewService(a.store, deploy.SimulationExecutor{Delay: time.Millisecond}), false, AuthConfig{AdminToken: "secret"}, slog.New(slog.NewTextHandler(io.Discard, nil)), EventConfig{Vault: a.eventConfig.Vault})
	if err := restarted.ProcessWebhooksOnce(ctx); err != nil {
		t.Fatal(err)
	}
	exists, err = a.store.IncomingEventExists(ctx, core.EventProviderGitHub, "comment::acme/service:123")
	if err != nil || !exists {
		t.Fatalf("restart did not process receipt: %t %v", exists, err)
	}
	items, err = a.store.ListWebhookDeliveries(ctx, "", 50)
	if err != nil || len(items) != 1 || items[0].State != "processed" || items[0].Attempts != 1 || items[0].Ciphertext != "" {
		t.Fatalf("completed receipt: %+v %v", items, err)
	}
	if strings.Contains(items[0].Result, "do-not-expose") {
		t.Fatal("result persisted comment arguments")
	}
	for _, id := range []string{"delivery-1", "changed-unsigned-delivery-header"} {
		rr = send(restarted, "issue_comment", id, webhookCommentBody)
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"duplicate":true`) {
			t.Fatalf("replay: %d %s", rr.Code, rr.Body.String())
		}
	}
	rr = send(restarted, "issue_comment", "delivery-1", strings.ReplaceAll(webhookCommentBody, "123", "124"))
	if rr.Code != 409 {
		t.Fatalf("changed body accepted under old identity: %d", rr.Code)
	}
	if err := a.store.RetainWebhookDeliveries(ctx, time.Now().Add(40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rr = send(restarted, "issue_comment", "another-header", webhookCommentBody)
	if rr.Code != 200 {
		t.Fatalf("retention allowed replay: %d", rr.Code)
	}
	if err := restarted.ProcessWebhooksOnce(ctx); err != nil {
		t.Fatal(err)
	}
	items, _ = a.store.ListWebhookDeliveries(ctx, "", 50)
	if len(items) != 1 || items[0].Attempts != 1 {
		t.Fatal("replay scheduled work")
	}
	raw := serviceRequestTest(t, restarted, "GET", "/api/v1/events/deliveries", nil, 200)
	if strings.Contains(string(raw), "do-not-expose") || strings.Contains(string(raw), "bodyDigest") || strings.Contains(string(raw), "leaseToken") {
		t.Fatal("delivery inventory exposed private fields")
	}
}

func TestWebhookValidationNeverPersistsInvalidOrUnsupportedInput(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	secret := "webhook-secret"
	for _, test := range []struct {
		name, event, body string
		signature         bool
		want              int
	}{
		{"signature before parsing", "issue_comment", "{", false, 401},
		{"malformed signed JSON", "issue_comment", "{", true, 400},
		{"unsupported event", "ping", "{", true, 204},
		{"ordinary issue comment", "issue_comment", `{"action":"created","repository":{"full_name":"acme/app"},"issue":{"number":1},"comment":{"id":1}}`, true, 204},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := signedWebhookRequest(secret, test.event, "validation", []byte(test.body))
			if !test.signature {
				r.Header.Set("X-Hub-Signature-256", "sha256=00")
			}
			rr := httptest.NewRecorder()
			a.processGitHubWebhook(rr, r, secret, "", 0, nil, nil)
			if rr.Code != test.want {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
	items, err := a.store.ListWebhookDeliveries(ctx, "", 50)
	if err != nil || len(items) != 0 {
		t.Fatalf("invalid input mutated queue: %+v %v", items, err)
	}
	a.eventConfig.Vault = nil
	a.eventConfig.GitHubApps = nil
	rr := httptest.NewRecorder()
	a.processGitHubWebhook(rr, signedWebhookRequest(secret, "issue_comment", "no-vault", []byte(webhookCommentBody)), secret, "", 0, nil, nil)
	if rr.Code != 503 {
		t.Fatalf("unencrypted receipt accepted: %d", rr.Code)
	}
}

func TestWebhookDeliveryInventoryRequiresOwner(t *testing.T) {
	a := serviceTestAPI(t)
	rr := httptest.NewRecorder()
	a.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/events/deliveries", nil))
	if rr.Code != 401 {
		t.Fatalf("unauthenticated queue access: %d", rr.Code)
	}
	// Project viewers get sanitized project activity, not the controller-wide queue.
	user := core.User{ID: "webhook-viewer", Username: "webhook-viewer", DisplayName: "Viewer", SystemRole: "member", State: "active", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := a.store.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	token, err := a.createSession(context.Background(), user.ID, identityForUser(user))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/events/deliveries", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr = httptest.NewRecorder()
	a.ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatalf("member read global delivery inventory: %d", rr.Code)
	}
	var result []core.WebhookDelivery
	if err := json.Unmarshal(serviceRequestTest(t, a, "GET", "/api/v1/events/deliveries", nil, 200), &result); err != nil {
		t.Fatal(err)
	}
}
