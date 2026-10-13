package instanceconnection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/instancebridge"
)

func testManager(t *testing.T, lookup func(context.Context, string) (core.User, error)) *Manager {
	t.Helper()
	if lookup == nil {
		lookup = func(_ context.Context, id string) (core.User, error) {
			return core.User{ID: id, State: core.UserStateActive, SystemRole: core.UserRoleMember}, nil
		}
	}
	m, err := Open(Config{File: filepath.Join(t.TempDir(), "connection.json"), LookupUser: lookup, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Stop)
	return m
}
func enrollmentServer(t *testing.T, broker *instancebridge.Broker) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/instance-connections/enroll":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input["enrollmentToken"] != "once" {
				http.Error(w, "invalid", 401)
				return
			}
			if input["installationID"] == "" {
				t.Error("installation ID missing")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"instanceID": "instance", "credential": "private-credential", "generation": 1, "websocketURL": strings.Replace(server.URL, "http:", "ws:", 1) + "/api/v1/instance-connections/ws", "ownerSubject": "cp-owner"})
		case "/api/v1/instance-connections/policy":
			if r.Header.Get("Authorization") != "Bearer private-credential" {
				http.Error(w, "invalid", 401)
				return
			}
			w.WriteHeader(204)
		case "/api/v1/instance-connections/current":
			if broker != nil {
				broker.Disconnect("instance:1")
			}
			w.WriteHeader(204)
		case "/api/v1/instance-connections/ws":
			if r.Header.Get("Authorization") != "Bearer private-credential" || broker == nil {
				http.Error(w, "invalid", 401)
				return
			}
			broker.ServeConnection(w, r, "instance:1")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
func TestConnectionEnrollmentPersistsPrivateGrantAndExplicitMapping(t *testing.T) {
	m := testManager(t, nil)
	server := enrollmentServer(t, nil)
	status, err := m.Configure(context.Background(), Setup{ControlPlaneURL: server.URL, EnrollmentToken: "once", OwnerUserID: "local-owner"})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Configured || status.RemoteAccess || status.Connected {
		t.Fatalf("unexpected status: %+v", status)
	}
	if len(status.UserMappings) != 1 || status.UserMappings[0].Subject != "cp-owner" || status.UserMappings[0].UserID != "local-owner" {
		t.Fatalf("mapping missing: %+v", status.UserMappings)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-credential") || strings.Contains(string(raw), "once") {
		t.Fatal("credentials exposed in status")
	}
	info, err := os.Stat(m.config.File)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	reopened, err := Open(m.config)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status().InstallationID != status.InstallationID || !reopened.Status().Configured {
		t.Fatal("connection did not survive restart")
	}
	// Email/role claims are ignored, only a stable subject explicitly mapped by owner works.
	if _, err = m.mappedUser(context.Background(), m.revision, "some-other-owner"); err == nil {
		t.Fatal("remote access default unexpectedly enabled")
	}
}
func TestConnectionRejectsInactiveAndUnapprovedIdentity(t *testing.T) {
	lookup := func(_ context.Context, id string) (core.User, error) {
		if id == "disabled" {
			return core.User{ID: id, State: core.UserStateDisabled}, nil
		}
		return core.User{ID: id, State: core.UserStateActive, SystemRole: core.UserRoleMember}, nil
	}
	m := testManager(t, lookup)
	server := enrollmentServer(t, nil)
	_, err := m.Configure(context.Background(), Setup{ControlPlaneURL: server.URL, EnrollmentToken: "once", RemoteAccess: true, UserMappings: []UserMapping{{Subject: "cp-member", UserID: "disabled"}}})
	if err == nil {
		t.Fatal("disabled mapping accepted")
	}
	_, err = m.Configure(context.Background(), Setup{ControlPlaneURL: server.URL, EnrollmentToken: "once", RemoteAccess: true, UserMappings: []UserMapping{{Subject: "cp-member", UserID: "local-member"}}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	if err = m.authorize(context.Background(), m.revision, instancebridge.OperationUser, instancebridge.Identity{UserID: "not-linked", Email: "same@example.com", Role: "owner"}, req); err == nil {
		t.Fatal("spoofed provider role authorized")
	}
	if err = m.authorize(context.Background(), m.revision, instancebridge.OperationUser, instancebridge.Identity{UserID: "cp-member", Role: "owner"}, req); err != nil {
		t.Fatal(err)
	}
	actor, _ := m.mappedUser(context.Background(), m.revision, "cp-member")
	if actor.SystemRole != core.UserRoleMember {
		t.Fatal("provider role promoted local user")
	}
	if _, ok := m.Identity(req); ok {
		t.Fatal("untrusted browser request has remote identity")
	}
}
func TestConnectionRemoteRoutesCannotMintLocalCredentials(t *testing.T) {
	for _, target := range []string{"/api/v1/auth/login", "/api/v1/auth/setup", "/api/v1/auth/exchange", "/api/v1/auth/password", "/api/v1/instance-connection", "/api/v1/automation/service-accounts", "/api/v1/edge/nodes/id/enroll", "/api/v1/users/id", "/api/v1/apps/../auth/login"} {
		if allowedUser(httptest.NewRequest("POST", target, nil)) {
			t.Errorf("allowed %s", target)
		}
	}
	for _, target := range []string{"/api/v1/auth/me", "/api/v1/apps", "/api/v1/workflows/runs/run/logs"} {
		if !allowedUser(httptest.NewRequest("GET", target, nil)) {
			t.Errorf("denied %s", target)
		}
	}
	if !allowedWebhook(httptest.NewRequest("POST", "/api/v1/events/github/apps/id", nil)) || allowedWebhook(httptest.NewRequest("GET", "/api/v1/events/github", nil)) || allowedWebhook(httptest.NewRequest("POST", "/api/v1/auth/login", nil)) {
		t.Fatal("webhook route boundary failed")
	}
}
func TestConnectionPolicyRevokesLocallyWhileControlPlaneOffline(t *testing.T) {
	m := testManager(t, nil)
	server := enrollmentServer(t, nil)
	_, err := m.Configure(context.Background(), Setup{ControlPlaneURL: server.URL, EnrollmentToken: "once", RemoteAccess: true, UserMappings: []UserMapping{{Subject: "cp-user", UserID: "local-user"}}})
	if err != nil {
		t.Fatal(err)
	}
	oldRevision := m.revision
	server.Close()
	_, err = m.Configure(context.Background(), Setup{ControlPlaneURL: server.URL, RemoteAccess: false})
	if err != nil {
		t.Fatal(err)
	}
	if m.Status().RemoteAccess {
		t.Fatal("remote access remained enabled")
	}
	if _, err = m.mappedUser(context.Background(), oldRevision, "cp-user"); err == nil {
		t.Fatal("old connection still grants access")
	}
	if err = m.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.Status().Configured {
		t.Fatal("disconnect retained connection")
	}
	raw, err := os.ReadFile(m.config.File)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-credential") {
		t.Fatal("disconnect retained credential")
	}
	local, err := m.config.LookupUser(context.Background(), "local-user")
	if err != nil || local.State != core.UserStateActive {
		t.Fatal("disconnect affected local account")
	}
}
func TestConnectionOutboundTunnelIdentityWebhookAndDisconnect(t *testing.T) {
	broker := instancebridge.NewBroker()
	defer broker.Close()
	server := enrollmentServer(t, broker)
	var mu sync.Mutex
	enabled := true
	m := testManager(t, func(_ context.Context, id string) (core.User, error) {
		mu.Lock()
		defer mu.Unlock()
		if !enabled {
			return core.User{}, errors.New("disabled")
		}
		return core.User{ID: id, State: core.UserStateActive, SystemRole: core.UserRoleMember}, nil
	})
	_, err := m.Configure(context.Background(), Setup{ControlPlaneURL: server.URL, EnrollmentToken: "once", RemoteAccess: true, UserMappings: []UserMapping{{Subject: "cp-user", UserID: "local-user"}}})
	if err != nil {
		t.Fatal(err)
	}
	streaming := make(chan struct{})
	cancelled := make(chan struct{})
	m.Start(context.Background(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/events/github" {
			w.WriteHeader(202)
			return
		}
		identity, ok := m.Identity(r)
		if !ok || identity.ID != "local-user" || identity.SystemRole != core.UserRoleMember {
			http.Error(w, "identity missing", 500)
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			http.Error(w, "credentials forwarded", 500)
			return
		}
		if r.URL.Path == "/api/v1/logs" {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			close(streaming)
			<-r.Context().Done()
			close(cancelled)
			return
		}
		_, _ = io.WriteString(w, identity.ID)
	}))
	deadline := time.Now().Add(3 * time.Second)
	for !broker.Connected("instance:1") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !broker.Connected("instance:1") || !m.Status().Connected {
		t.Fatal("outbound connection did not establish")
	}
	r := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	r.Header.Set("Authorization", "Bearer local-admin")
	r.Header.Set("Cookie", "secret=local")
	w := httptest.NewRecorder()
	broker.Proxy(w, r, "instance:1", instancebridge.Identity{UserID: "cp-user", Role: "owner"})
	if w.Code != 200 || w.Body.String() != "local-user" {
		t.Fatalf("response %d %s", w.Code, w.Body.String())
	}
	webhook := httptest.NewRecorder()
	broker.ProxyOperation(webhook, httptest.NewRequest("POST", "/api/v1/events/github", strings.NewReader("{}")), "instance:1", instancebridge.OperationWebhook, instancebridge.Identity{})
	if webhook.Code != 202 {
		t.Fatalf("webhook %d", webhook.Code)
	}
	done := make(chan struct{})
	go func() {
		broker.Proxy(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/logs", nil), "instance:1", instancebridge.Identity{UserID: "cp-user"})
		close(done)
	}()
	select {
	case <-streaming:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not start")
	}
	if err = m.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect did not cancel remote stream")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("proxy did not finish")
	}
	if broker.Connected("instance:1") {
		t.Fatal("socket survived disconnect")
	}
}

func TestRevokedCredentialFileCannotReconnectAfterRestart(t *testing.T) {
	m := testManager(t, nil)
	server := enrollmentServer(t, nil)
	if _, err := m.Configure(context.Background(), Setup{ControlPlaneURL: server.URL, EnrollmentToken: "once"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(m.config.File, m.config.File+".revoked"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(m.config)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status().Configured || reopened.current.Credential != "" {
		t.Fatal("revoked credential was resurrected")
	}
}

func TestInvalidSavedConnectionCanBeRepairedWithoutBlockingLocalAccess(t *testing.T) {
	for _, kind := range []string{"invalid-json", "unsafe-permissions", "invalid-endpoint"} {
		t.Run(kind, func(t *testing.T) {
			original := testManager(t, nil)
			server := enrollmentServer(t, nil)
			if _, err := original.Configure(context.Background(), Setup{ControlPlaneURL: server.URL, EnrollmentToken: "once"}); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "invalid-json":
				if err := os.WriteFile(original.config.File, []byte(`{"credential":`), 0600); err != nil {
					t.Fatal(err)
				}
			case "unsafe-permissions":
				if err := os.Chmod(original.config.File, 0644); err != nil {
					t.Fatal(err)
				}
			case "invalid-endpoint":
				broken := original.current
				broken.ControlPlaneURL = "https://control.example/incorrect/path"
				if err := original.save(broken); err != nil {
					t.Fatal(err)
				}
			}
			repaired, err := Open(original.config)
			if err != nil {
				t.Fatal("optional connection stopped the local installation", err)
			}
			if repaired.Status().Configured || repaired.Status().LastError == "" || repaired.current.Credential != "" {
				t.Fatal("invalid saved grant was not disabled")
			}
			if local, err := repaired.config.LookupUser(context.Background(), "local-user"); err != nil || local.State != core.UserStateActive {
				t.Fatal("native local account was affected")
			}
			if _, err = repaired.Configure(context.Background(), Setup{ControlPlaneURL: server.URL, EnrollmentToken: "once"}); err != nil {
				t.Fatal("owner could not reconnect", err)
			}
			if !repaired.Status().Configured || repaired.Status().LastError != "" {
				t.Fatal("owner repair did not clear error")
			}
		})
	}
}

func TestEnrollmentRejectsUnexpectedInstanceBeforeSavingGrant(t *testing.T) {
	m := testManager(t, nil)
	server := enrollmentServer(t, nil)
	if _, err := m.Configure(context.Background(), Setup{ControlPlaneURL: server.URL, EnrollmentToken: "once", ExpectedInstanceID: "other", OwnerUserID: "local-owner"}); err == nil {
		t.Fatal("accepted another instance")
	}
	if m.Status().Configured {
		t.Fatal("saved grant for unexpected instance")
	}
}
