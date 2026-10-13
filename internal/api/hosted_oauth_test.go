package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/store"
)

type hostedOAuthTransport func(*http.Request) (*http.Response, error)

func (f hostedOAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHostedOAuthRequiresBrowserProofAndExplicitIdentity(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	data, err := store.Open(ctx, filepath.Join(root, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "key")
	if err = os.WriteFile(key, []byte("0123456789abcdef0123456789abcde!"), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(key)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := vault.Encrypt("auth-provider:github", []byte("client-secret"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	provider := core.AuthProvider{ID: "github", Name: "GitHub", Type: core.AuthProviderGitHub, BaseURL: "https://github.example.test", APIURL: "https://github.example.test/api/v3", ClientID: "client", EncryptedClientSecret: encrypted, Provisioning: core.AuthProvisionExisting, State: core.AuthProviderStateReady, CreatedAt: now, UpdatedAt: now}
	if err = data.CreateAuthProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	if err = data.CreateUser(ctx, core.User{ID: "owner", Username: "owner", Email: "owner@example.test", DisplayName: "Owner", State: core.UserStateActive, SystemRole: core.UserRoleOwner, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	exchanges := 0
	http.DefaultTransport = hostedOAuthTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Path {
		case "/login/oauth/access_token":
			exchanges++
			body = `{"access_token":"github-token"}`
		case "/api/v3/user":
			body = `{"id":42,"login":"owner","name":"Owner"}`
		case "/api/v3/user/emails":
			body = `[{"email":"owner@example.test","primary":true,"verified":true}]`
		default:
			t.Errorf("unexpected GitHub request %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	completed := false
	a := &API{store: data, eventConfig: EventConfig{Vault: vault}, auth: AuthConfig{PublicURL: "https://tenant.dispatch.example.test", Hosted: &HostedAuth{CompleteExternal: func(w http.ResponseWriter, r *http.Request, user, provider, subject string) error {
		completed = user == "owner" && provider == "github" && subject == "42"
		return nil
	}}}, oauthStates: map[string]oauthState{}}
	callback := func(cookie string) *httptest.ResponseRecorder {
		a.oauthStates["state"] = oauthState{ProviderID: "github", Verifier: "verifier", ExpiresAt: time.Now().Add(time.Minute)}
		r := httptest.NewRequest("GET", "https://tenant.dispatch.example.test/api/v1/auth/callback?state=state&code=code", nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-dispatch-oauth", Value: cookie})
		}
		w := httptest.NewRecorder()
		a.completeOAuth(w, r)
		return w
	}
	callback("wrong")
	if exchanges != 0 || completed {
		t.Fatal("foreign browser reached exchange")
	}
	w := callback("state")
	if exchanges != 1 || completed || !strings.Contains(w.Header().Get("Location"), "auth_error=") {
		t.Fatal("email match granted access")
	}
	if _, err = data.GetExternalIdentity(ctx, "github", "42"); err == nil {
		t.Fatal("email match linked identity")
	}
	if err = data.UpsertExternalIdentity(ctx, core.ExternalIdentity{ProviderID: "github", Subject: "42", UserID: "owner", Login: "owner", Email: "owner@example.test", CreatedAt: now, LastLogin: now}); err != nil {
		t.Fatal(err)
	}
	w = callback("state")
	if !completed || w.Header().Get("Location") != "https://tenant.dispatch.example.test/" {
		t.Fatalf("linked login: %s", w.Header().Get("Location"))
	}
	// The hosted path returns a host-only session through its callback, never a
	// standalone bearer-token handoff that could bypass central membership.
	if len(a.oauthCodes) != 0 {
		raw, _ := json.Marshal(a.oauthCodes)
		t.Fatalf("local token handoff issued: %s", raw)
	}
}
