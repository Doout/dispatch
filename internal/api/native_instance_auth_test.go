package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/instancebridge"
	"github.com/doout/dispatch/internal/instanceconnection"
	"golang.org/x/crypto/bcrypt"
)

func TestInstanceNativeAuthenticationOverOutboundConnection(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	key := filepath.Join(root, "key")
	if err := os.WriteFile(key, []byte("0123456789abcdef0123456789abcde!"), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(key)
	if err != nil {
		t.Fatal(err)
	}
	var a *API
	m, err := instanceconnection.Open(instanceconnection.Config{File: filepath.Join(root, "connection.json"), Client: &http.Client{Transport: http.DefaultTransport}, AllowInsecure: true, LookupUser: func(ctx context.Context, id string) (core.User, error) { return a.store.GetUser(ctx, id) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Stop)
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "static-owner", PublicURL: "https://private.example.test"}, false, EventConfig{InstanceConnection: m, Vault: vault})
	t.Cleanup(cleanup)
	a = handler.(*API)
	now := time.Now().UTC()
	hash, err := bcrypt.GenerateFromPassword([]byte("local-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	member := core.User{ID: "local-member", Username: "member", Email: "member@example.test", DisplayName: "Member", PasswordHash: string(hash), State: core.UserStateActive, SystemRole: core.UserRoleMember, CreatedAt: now, UpdatedAt: now}
	if err = a.store.CreateUser(ctx, member); err != nil {
		t.Fatal(err)
	}
	encrypted, err := vault.Encrypt("auth-provider:github", []byte("provider-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if err = a.store.CreateAuthProvider(ctx, core.AuthProvider{ID: "github", Name: "Company GitHub", Type: core.AuthProviderGitHub, BaseURL: "https://github.example.test", APIURL: "https://github.example.test/api/v3", ClientID: "client", EncryptedClientSecret: encrypted, Provisioning: core.AuthProvisionExisting, State: core.AuthProviderStateReady, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = a.store.UpsertExternalIdentity(ctx, core.ExternalIdentity{ProviderID: "github", Subject: "42", UserID: member.ID, Login: "member", Email: member.Email, CreatedAt: now, LastLogin: now}); err != nil {
		t.Fatal(err)
	}
	b := instancebridge.NewBroker()
	t.Cleanup(b.Close)
	var cp *httptest.Server
	cp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/instance-connections/enroll":
			json.NewEncoder(w).Encode(map[string]any{"instanceID": "instance", "instanceSlug": "agentops", "rootDomain": "doodship.test", "generation": 1, "credential": "connection-only", "websocketURL": strings.Replace(cp.URL, "http:", "ws:", 1) + "/api/v1/instance-connections/ws"})
		case "/api/v1/instance-connections/ws":
			b.ServeConnection(w, r, "instance")
		default:
			w.WriteHeader(204)
		}
	}))
	t.Cleanup(cp.Close)
	if _, err = m.Configure(ctx, instanceconnection.Setup{ControlPlaneURL: cp.URL, EnrollmentToken: "once", RemoteAccess: true}); err != nil {
		t.Fatal(err)
	}
	m.Start(ctx, handler)
	deadline := time.Now().Add(3 * time.Second)
	for (!b.NativeSignInAvailable("instance") || !m.Status().Connected) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !b.NativeSignInAvailable("instance") {
		t.Fatal("native sign-in capability not advertised")
	}
	const origin = "https://agentops.doodship.test"
	call := func(method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer static-owner")
		r.Header.Set("X-Doodship-Role", "owner")
		r.AddCookie(&http.Cookie{Name: "__Host-dispatch-tenant", Value: "cloud-session"})
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		b.ProxyOperation(w, r, "instance", instancebridge.OperationNative, instancebridge.Identity{UserID: "cloud-owner", Role: "owner"})
		return w
	}
	cookie := func(w *httptest.ResponseRecorder, name string) *http.Cookie {
		t.Helper()
		for _, c := range w.Result().Cookies() {
			if c.Name == name {
				if !c.Secure || !c.HttpOnly || c.Domain != "" || c.Path != "/" {
					t.Fatal("cookie boundary missing")
				}
				return c
			}
		}
		t.Fatalf("missing cookie %s", name)
		return nil
	}
	if w := call("GET", "/api/v1/auth/providers", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Company GitHub") || strings.Contains(w.Body.String(), "provider-secret") {
		t.Fatalf("provider discovery: %d %s", w.Code, w.Body.String())
	}
	for _, p := range []string{"/api/v1/overview", "/api/v1/auth/me"} {
		if w := call("GET", p, ""); w.Code != 401 {
			t.Fatalf("static/claimed owner authorized %s: %d", p, w.Code)
		}
	}
	for _, p := range []string{"/api/v1/auth/setup", "/api/v1/instance-connection", "/api/v1/service-accounts"} {
		if w := call("POST", p, "{}"); w.Code != 403 {
			t.Fatalf("anonymous management %s: %d", p, w.Code)
		}
	}
	exchanges := 0
	original := http.DefaultTransport
	http.DefaultTransport = hostedOAuthTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "github.example.test" {
			return original.RoundTrip(r)
		}
		body := ""
		switch r.URL.Path {
		case "/login/oauth/access_token":
			exchanges++
			if err := r.ParseForm(); err != nil || r.Form.Get("redirect_uri") != origin+"/api/v1/auth/callback" {
				t.Error("managed OAuth callback lost")
			}
			body = `{"access_token":"github-token"}`
		case "/api/v3/user":
			body = `{"id":42,"login":"member","name":"Member"}`
		case "/api/v3/user/emails":
			body = `[{"email":"member@example.test","primary":true,"verified":true}]`
		default:
			t.Errorf("unexpected provider request %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	start := func() (string, *http.Cookie) {
		t.Helper()
		w := call("POST", "/api/v1/auth/providers/github/start", "{}")
		if w.Code != 200 {
			t.Fatalf("start: %d %s", w.Code, w.Body.String())
		}
		var value struct {
			URL string `json:"authorizationUrl"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(value.URL)
		if err != nil {
			t.Fatal(err)
		}
		if u.Query().Get("redirect_uri") != origin+"/api/v1/auth/callback" || u.Query().Get("code_challenge_method") != "S256" {
			t.Fatal("callback origin/PKCE missing")
		}
		return u.Query().Get("state"), cookie(w, instancebridge.NativeOAuthCookie)
	}
	state, _ := start()
	if w := call("GET", "/api/v1/auth/callback?state="+state+"&code=code", "", &http.Cookie{Name: instancebridge.NativeOAuthCookie, Value: "wrong"}); w.Code != 303 || exchanges != 0 {
		t.Fatal("foreign browser completed OAuth")
	}
	state, proof := start()
	direct := httptest.NewRecorder()
	handler.ServeHTTP(direct, httptest.NewRequest("GET", "https://private.example.test/api/v1/auth/callback?state="+state+"&code=code", nil))
	if exchanges != 0 {
		t.Fatal("managed state accepted by direct callback")
	}
	state, proof = start()
	w := call("GET", "/api/v1/auth/callback?state="+state+"&code=code", "", proof)
	if w.Code != 303 || exchanges != 1 {
		t.Fatalf("callback: %d %s", w.Code, w.Header().Get("Location"))
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host != origin || u.Query().Get("auth_code") == "" {
		t.Fatal("callback left managed instance")
	}
	body, _ := json.Marshal(map[string]string{"code": u.Query().Get("auth_code")})
	if w := call("POST", "/api/v1/auth/exchange", string(body), &http.Cookie{Name: instancebridge.NativeOAuthCookie, Value: "wrong"}); w.Code != 401 {
		t.Fatal("foreign browser exchanged handoff")
	}
	w = call("POST", "/api/v1/auth/exchange", string(body), proof)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"token":""}` {
		t.Fatalf("exchange exposed token or failed: %d %s", w.Code, w.Body.String())
	}
	session := cookie(w, instancebridge.NativeSessionCookie)
	if w := call("POST", "/api/v1/auth/exchange", string(body), proof); w.Code != 401 {
		t.Fatal("handoff replay accepted")
	}
	if w := call("GET", "/api/v1/auth/me", "", session); w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"local-member"`) || !strings.Contains(w.Body.String(), `"systemRole":"member"`) {
		t.Fatalf("local identity lost: %d %s", w.Code, w.Body.String())
	}
	if w := call("GET", "/api/v1/access", "", session); w.Code != 403 {
		t.Fatal("member promoted to owner", w.Code)
	}
	for _, route := range []string{"/api/v1/users", "/api/v1/teams", "/api/v1/role-assignments", "/api/v1/auth/providers"} {
		if w := call("POST", route, "{}"); w.Code != 401 {
			t.Fatalf("anonymous access management %s: %d", route, w.Code)
		}
		if w := call("POST", route, "{}", session); w.Code != 403 {
			t.Fatalf("member access management %s: %d", route, w.Code)
		}
	}
	owner := core.User{ID: "local-owner", Username: "owner", DisplayName: "Owner", PasswordHash: string(hash), State: core.UserStateActive, SystemRole: core.UserRoleOwner, CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(ctx, owner); err != nil {
		t.Fatal(err)
	}
	ownerLogin := call("POST", "/api/v1/auth/login", `{"username":"owner","password":"local-password"}`)
	if ownerLogin.Code != 200 {
		t.Fatalf("owner login: %d %s", ownerLogin.Code, ownerLogin.Body.String())
	}
	ownerSession := cookie(ownerLogin, instancebridge.NativeSessionCookie)
	if w := call("GET", "/api/v1/access", "", ownerSession); w.Code != 200 || !strings.Contains(w.Body.String(), `"localAccountsAvailable":true`) {
		t.Fatalf("access capabilities: %d %s", w.Code, w.Body.String())
	}
	for i, callbackSession := range []*http.Cookie{nil, session, ownerSession} {
		state := fmt.Sprintf("manifest-owner-%d", i)
		actorID := owner.ID
		if i == 2 {
			actorID = "different-owner"
		}
		a.authManifestStates[state] = authProviderManifestState{ActorID: actorID, ExpiresAt: time.Now().Add(time.Hour)}
		var cookies []*http.Cookie
		if callbackSession != nil {
			cookies = append(cookies, callbackSession)
		}
		w := call("GET", "/api/v1/auth/providers/manifest/callback?state="+state+"&code=setup", "", cookies...)
		if w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "authProviderStatus=error") {
			t.Fatalf("manifest callback accepted a missing, member, or foreign owner session: %d", w.Code)
		}
	}
	created := call("POST", "/api/v1/users", `{"username":"teammate","displayName":"Teammate","password":"teammate-password","systemRole":"member","state":"active"}`, ownerSession)
	if created.Code != 201 || strings.Contains(created.Body.String(), "teammate-password") {
		t.Fatalf("owner create local user: %d %s", created.Code, created.Body.String())
	}
	if w := call("POST", "/api/v1/auth/login", `{"username":"teammate","password":"teammate-password"}`); w.Code != 200 {
		t.Fatalf("created local user cannot sign in: %d %s", w.Code, w.Body.String())
	}
	member.State = core.UserStateDisabled
	if err := a.store.UpdateUser(ctx, member); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/api/v1/auth/me", "", session); w.Code != 401 {
		t.Fatal("disabled native user accepted", w.Code)
	}
	if w := call("POST", "/api/v1/auth/logout", "{}", session); w.Code != 204 {
		t.Fatal("logout did not revoke disabled session", w.Code)
	}
	member.State = core.UserStateActive
	if err := a.store.UpdateUser(ctx, member); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/api/v1/auth/me", "", session); w.Code != 401 {
		t.Fatal("logged-out native session accepted")
	}
	w = call("POST", "/api/v1/auth/login", `{"username":"member","password":"local-password"}`)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"token":""}` {
		t.Fatalf("password login failed: %d %s", w.Code, w.Body.String())
	}
	session = cookie(w, instancebridge.NativeSessionCookie)
	for _, foreign := range []struct{ host, origin string }{
		{"agentops.doodship.test", "https://other.doodship.test"},
		{"other.doodship.test", origin},
		{"agentops.doodship.test", ""},
	} {
		r := httptest.NewRequest("POST", "https://"+foreign.host+"/api/v1/auth/logout", strings.NewReader("{}"))
		r.Header.Set("Origin", foreign.origin)
		r.AddCookie(session)
		w := httptest.NewRecorder()
		b.ProxyOperation(w, r, "instance", instancebridge.OperationNative, instancebridge.Identity{})
		if w.Code != 403 {
			t.Fatal("foreign host/origin accepted", w.Code)
		}
	}
	if w := call("GET", "/api/v1/auth/me", "", session); w.Code != 200 {
		t.Fatal("foreign logout revoked local session", w.Code)
	}
	if _, err := m.Configure(ctx, instanceconnection.Setup{ControlPlaneURL: cp.URL, RemoteAccess: false, DirectURL: "https://private.example.test"}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for !b.NativeSignInAvailable("instance") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if w := call("GET", "/api/v1/auth/me", "", session); w.Code != 403 && w.Code != 502 && w.Code != 503 {
		t.Fatal("disabled remote access accepted native login", w.Code)
	}
}
