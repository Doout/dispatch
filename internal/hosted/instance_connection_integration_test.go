package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/instancebridge"
	"github.com/doout/dispatch/internal/tenancy"
)

// The loopback adapter represents TLS ingress with the platform's registered
// host. The customer has no listener: its handler runs behind its outbound socket.
func TestConnectedInstanceRoutesLiveTrafficAndRevokesStreams(t *testing.T) {
	f := newHostedFixture(t)
	f.server.Config.BackendMode = tenancy.BackendConnected
	tenant := f.tenant(t, "connected", "owner-a")
	other := f.tenant(t, "another", "owner-b")
	token, _, err := f.catalog.CreateInstanceEnrollment(context.Background(), "owner-a", tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	connection, credential, err := f.catalog.EnrollInstance(context.Background(), token, "installation", "https://private.example.test", true)
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch instancebridge.OperationFromContext(r.Context()) {
		case instancebridge.OperationWebhook:
			if r.Header.Get("X-Hub-Signature-256") != "sha256=signed" {
				http.Error(w, "invalid signature", 403)
				return
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != "original webhook" {
				http.Error(w, "changed payload", 400)
				return
			}
			w.WriteHeader(202)
		case instancebridge.OperationNetwork:
			if r.Header.Get("Authorization") != "Bearer node-credential" {
				http.Error(w, "invalid node", 401)
				return
			}
			w.WriteHeader(204)
		default:
			actor, _ := instancebridge.IdentityFromContext(r.Context())
			if r.URL.Path == "/api/v1/live" {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: live\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(stopped)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"actor": actor.UserID})
		}
	})
	var gateway *httptest.Server
	gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/instance-connections/ws" {
			r.Host = f.server.Config.RootDomain
			r.Header.Set("Origin", f.server.Config.Origin())
		}
		f.server.ServeHTTP(w, r)
	}))
	t.Cleanup(gateway.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	connector, err := instancebridge.NewConnector(instancebridge.ConnectorConfig{
		Endpoint: gateway.URL + "/api/v1/instance-connections/ws", Credential: credential, AllowInsecure: true, RemoteAccess: true, Handler: backend,
		Authorize: func(_ context.Context, op instancebridge.Operation, actor instancebridge.Identity, r *http.Request) error {
			if op == instancebridge.OperationUser && actor.UserID != "owner-a" {
				return fmt.Errorf("unapproved account")
			}
			if op == instancebridge.OperationNetwork && !instancebridge.NetworkRequestAllowed(r) {
				return fmt.Errorf("unapproved route")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- connector.Connect(ctx) }()
	t.Cleanup(func() {
		cancel()
		f.server.Bridge.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("outbound connector did not stop")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for !f.server.Bridge.Connected(connectionKey(connection)) {
		if time.Now().After(deadline) {
			t.Fatal("outbound socket did not connect")
		}
		time.Sleep(10 * time.Millisecond)
	}
	host := tenant.Slug + "." + f.server.Config.RootDomain
	session := f.session(t, "owner-a", tenancy.TenantAudience(tenant.ID))
	if w := f.request(t, host, "GET", "/api/v1/private", session, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "owner-a") {
		t.Fatalf("user request: %d %s", w.Code, w.Body.String())
	}
	if w := f.request(t, other.Slug+"."+f.server.Config.RootDomain, "GET", "/api/v1/private", session, nil); w.Code != 403 && w.Code != 401 {
		t.Fatalf("session crossed instances: %d", w.Code)
	}
	if err = f.catalog.UpdateInstancePolicy(context.Background(), connection, "https://private.example.test", false); err != nil {
		t.Fatal(err)
	}
	if w := f.request(t, host, "GET", "/api/v1/private", session, nil); w.Code != 403 {
		t.Fatalf("disabled UI allowed: %d", w.Code)
	}
	req := httptest.NewRequest("POST", "https://"+host+"/api/v1/events/github", strings.NewReader("original webhook"))
	req.Header.Set("X-Hub-Signature-256", "sha256=signed")
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, req)
	if w.Code != 202 {
		t.Fatalf("webhook while remote access disabled: %d %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("GET", "https://"+host+"/api/v1/edge/nodes/node/jobs/next", nil)
	req.Header.Set("Authorization", "Bearer node-credential")
	w = httptest.NewRecorder()
	f.server.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("node traffic: %d %s", w.Code, w.Body.String())
	}
	if err = f.catalog.UpdateInstancePolicy(context.Background(), connection, "https://private.example.test", true); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("GET", gateway.URL+"/api/v1/live", nil)
	request.Host = host
	request.Header.Set("Authorization", "Bearer "+session)
	response, err := gateway.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	buf := make([]byte, len("data: live\n\n"))
	if _, err = io.ReadFull(response.Body, buf); err != nil || string(buf) != "data: live\n\n" {
		t.Fatalf("live log: %q %v", buf, err)
	}
	ownerSession := f.session(t, "owner-a", tenancy.AudiencePlatform)
	w = f.request(t, f.server.Config.RootDomain, "DELETE", "/api/v1/tenants/"+tenant.ID+"/connection", ownerSession, nil)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect did not stop active customer request")
	}
	if f.server.Bridge.Connected(connectionKey(connection)) {
		t.Fatal("revoked connection remained active")
	}
	if _, err = f.catalog.AuthenticateInstance(context.Background(), credential); err == nil {
		t.Fatal("revoked credential still authenticates")
	}
	if f.opens.Load() != 0 {
		t.Fatal("traffic opened central operational data")
	}
}
