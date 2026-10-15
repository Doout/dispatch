package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/instancebridge"
	"github.com/doout/dispatch/internal/tenancy"
)

func TestNativeInstanceSignInRoutesAndLogout(t *testing.T) {
	f := newHostedFixture(t)
	f.server.Config.BackendMode = tenancy.BackendConnected
	tenant := f.tenant(t, "native", "owner-a")
	token, _, err := f.catalog.CreateInstanceEnrollment(context.Background(), "owner-a", tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	connection, credential, err := f.catalog.EnrollInstance(context.Background(), token, "installation", "https://private.example.test", true)
	if err != nil {
		t.Fatal(err)
	}
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if instancebridge.OperationFromContext(r.Context()) != instancebridge.OperationNative {
			t.Error("native request fell back to the Doodship account")
			http.Error(w, "wrong identity", 403)
			return
		}
		if actor, ok := instancebridge.IdentityFromContext(r.Context()); ok || actor.UserID != "" || r.Header.Get("Authorization") != "" {
			t.Error("cloud identity crossed native authentication")
		}
		if _, err := r.Cookie(tenantCookie); err == nil {
			t.Error("cloud cookie forwarded")
		}
		switch r.URL.Path {
		case "/api/v1/auth/providers":
			fmt.Fprint(w, `[{"id":"github","name":"Company GitHub"}]`)
		case "/api/v1/auth/logout":
			cookie, err := r.Cookie(instancebridge.NativeSessionCookie)
			if err != nil || cookie.Value != "native-session" {
				t.Error("logout lost native session")
			}
			setSession(w, instancebridge.NativeSessionCookie, "", -1)
			w.WriteHeader(204)
		default:
			http.Error(w, "invalid local session", 401)
		}
	})
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = f.server.Config.RootDomain
		r.Header.Set("Origin", f.server.Config.Origin())
		f.server.ServeHTTP(w, r)
	}))
	t.Cleanup(gateway.Close)
	connector, err := instancebridge.NewConnector(instancebridge.ConnectorConfig{
		Endpoint: gateway.URL + "/api/v1/instance-connections/ws", Credential: credential, AllowInsecure: true, RemoteAccess: true, Handler: backend,
		Authorize: func(context.Context, instancebridge.Operation, instancebridge.Identity, *http.Request) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- connector.Connect(ctx) }()
	t.Cleanup(func() {
		cancel()
		f.server.Bridge.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("connector did not stop")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for !f.server.Bridge.NativeSignInAvailable(connectionKey(connection)) {
		if time.Now().After(deadline) {
			t.Fatal("native capability missing")
		}
		time.Sleep(10 * time.Millisecond)
	}
	host := tenant.Slug + "." + f.server.Config.RootDomain
	cloudSession := f.session(t, "owner-a", tenancy.TenantAudience(tenant.ID))
	request := func(method, path, native string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://"+host+path, nil)
		r.Header.Set("Origin", "https://"+host)
		r.Header.Set("Authorization", "Bearer "+cloudSession)
		r.AddCookie(&http.Cookie{Name: tenantCookie, Value: cloudSession})
		if native != "" {
			r.AddCookie(&http.Cookie{Name: instancebridge.NativeSessionCookie, Value: native})
		}
		w := httptest.NewRecorder()
		f.server.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/v1/auth/providers", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Company GitHub") {
		t.Fatalf("provider discovery: %d %s", w.Code, w.Body.String())
	}
	if w := request("GET", "/api/v1/overview", "invalid"); w.Code != 401 {
		t.Fatalf("invalid native session fell back: %d", w.Code)
	}
	w := request("GET", "/api/v1/hosted", "native-session")
	var metadata struct {
		Native         bool   `json:"nativeSignInAvailable"`
		BackendMode    string `json:"backendMode"`
		Authentication string `json:"authentication"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &metadata); err != nil || !metadata.Native || metadata.Authentication != "instance" || metadata.BackendMode != tenancy.BackendConnected {
		t.Fatal("native metadata missing")
	}
	w = request("POST", "/api/v1/hosted/auth/logout", "native-session")
	if w.Code != 204 {
		t.Fatalf("logout: %d %s", w.Code, w.Body.String())
	}
	cleared := map[string]bool{}
	for _, cookie := range w.Result().Cookies() {
		cleared[cookie.Name] = cookie.Value == "" && cookie.MaxAge < 0
	}
	if !cleared[tenantCookie] || !cleared[instancebridge.NativeSessionCookie] {
		t.Fatal("logout did not clear both sessions")
	}
	if _, err := f.catalog.AuthenticateSession(context.Background(), cloudSession, tenancy.TenantAudience(tenant.ID)); err == nil {
		t.Fatal("cloud session not revoked")
	}
	w = request("GET", "/api/v1/hosted/auth/start", "invalid")
	if w.Code != 303 {
		t.Fatal("Doodship handoff failed")
	}
	cleared = map[string]bool{}
	for _, cookie := range w.Result().Cookies() {
		cleared[cookie.Name] = cookie.Value == "" && cookie.MaxAge < 0
	}
	if !cleared[instancebridge.NativeSessionCookie] {
		t.Fatal("Doodship sign-in retained native session")
	}
	if err := f.catalog.UpdateInstancePolicy(context.Background(), connection, "https://private.example.test", false); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/deployments", "native-session")
	if w.Code != 303 || w.Header().Get("Location") != "https://private.example.test" {
		t.Fatal("disabled remote access did not redirect")
	}
	if w := request("GET", "/api/v1/auth/providers", "native-session"); w.Code != 403 {
		t.Fatal("disabled remote access allowed native discovery")
	}
	if f.opens.Load() != 0 {
		t.Fatal("native sign-in opened central operational data")
	}
}
