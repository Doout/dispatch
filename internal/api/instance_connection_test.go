package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/instancebridge"
	"github.com/doout/dispatch/internal/instanceconnection"
)

func TestInstanceConnectionOwnerSetupAndRemoteAuthentication(t *testing.T) {
	var controller *API
	manager, err := instanceconnection.Open(instanceconnection.Config{File: filepath.Join(t.TempDir(), "connection.json"), AllowInsecure: true, LookupUser: func(ctx context.Context, id string) (core.User, error) { return controller.store.GetUser(ctx, id) }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "local-owner-token"}, false, EventConfig{InstanceConnection: manager})
	defer cleanup()
	controller = handler.(*API)
	now := time.Now().UTC()
	if err = controller.store.CreateUser(context.Background(), core.User{ID: "local-member", Username: "local-member", DisplayName: "Local Member", State: core.UserStateActive, SystemRole: core.UserRoleMember, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = controller.store.CreateUser(context.Background(), core.User{ID: "local-owner", Username: "local-owner", DisplayName: "Local Owner", State: core.UserStateActive, SystemRole: core.UserRoleOwner, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/instance-connection", nil)
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, req)
	if unauthorized.Code != 401 {
		t.Fatalf("anonymous owner API %d", unauthorized.Code)
	}
	req = httptest.NewRequest("GET", "/api/v1/instance-connection", nil)
	req.Header.Set("Authorization", "Bearer local-owner-token")
	owner := httptest.NewRecorder()
	handler.ServeHTTP(owner, req)
	if owner.Code != 200 {
		t.Fatalf("owner API %d", owner.Code)
	}
	// Browser-supplied identity headers cannot authorize local requests.
	req = httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.Header.Set("X-Dispatch-User-ID", "local-member")
	req.Header.Set("Impersonate-User", "local-member")
	forged := httptest.NewRecorder()
	handler.ServeHTTP(forged, req)
	if forged.Code != 401 {
		t.Fatalf("forged identity accepted %d", forged.Code)
	}
	broker := instancebridge.NewBroker()
	defer broker.Close()
	var cp *httptest.Server
	cp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/instance-connections/enroll":
			_ = json.NewEncoder(w).Encode(map[string]any{"instanceID": "instance", "generation": 1, "credential": "connection-only", "websocketURL": strings.Replace(cp.URL, "http:", "ws:", 1) + "/api/v1/instance-connections/ws"})
		case "/api/v1/instance-connections/ws":
			broker.ServeConnection(w, r, "instance:1")
		default:
			w.WriteHeader(204)
		}
	}))
	defer cp.Close()
	if _, err = manager.Configure(context.Background(), instanceconnection.Setup{ControlPlaneURL: cp.URL, EnrollmentToken: "once", RemoteAccess: true, UserMappings: []instanceconnection.UserMapping{{Subject: "cp-member", UserID: "local-member"}, {Subject: "cp-owner", UserID: "local-owner"}}}); err != nil {
		t.Fatal(err)
	}
	manager.Start(context.Background(), handler)
	deadline := time.Now().Add(3 * time.Second)
	for (!manager.Status().Connected || !broker.Connected("instance:1")) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !manager.Status().Connected || !broker.Connected("instance:1") {
		t.Fatal("connection did not establish")
	}
	remote := httptest.NewRecorder()
	broker.Proxy(remote, httptest.NewRequest("GET", "/api/v1/auth/me", nil), "instance:1", instancebridge.Identity{UserID: "cp-member", Role: "owner"})
	if remote.Code != 200 || !strings.Contains(remote.Body.String(), `"id":"local-member"`) || !strings.Contains(remote.Body.String(), `"systemRole":"member"`) {
		t.Fatalf("remote account response %d %s", remote.Code, remote.Body.String())
	}
	remoteOwner := httptest.NewRecorder()
	broker.Proxy(remoteOwner, httptest.NewRequest("GET", "/api/v1/access", nil), "instance:1", instancebridge.Identity{UserID: "cp-member", Role: "owner"})
	if remoteOwner.Code != 403 {
		t.Fatalf("CP role promoted local member %d", remoteOwner.Code)
	}
	for _, route := range []string{"/api/v1/users", "/api/v1/teams", "/api/v1/role-assignments", "/api/v1/auth/providers"} {
		response := httptest.NewRecorder()
		broker.Proxy(response, httptest.NewRequest("POST", route, strings.NewReader("{}")), "instance:1", instancebridge.Identity{UserID: "cp-member", Role: "owner"})
		if response.Code != 403 {
			t.Fatalf("cloud role promoted local member on %s: %d", route, response.Code)
		}
	}
	created := httptest.NewRecorder()
	broker.Proxy(created, httptest.NewRequest("POST", "/api/v1/users", strings.NewReader(`{"username":"remote-created","password":"remote-created-password","displayName":"Created by local owner"}`)), "instance:1", instancebridge.Identity{UserID: "cp-owner", Role: "member"})
	if created.Code != 201 {
		t.Fatalf("mapped local owner cannot create user: %d %s", created.Code, created.Body.String())
	}
	for _, target := range []string{"/api/v1/instance-connection", "/api/v1/auth/login", "/api/v1/automation-accounts"} {
		response := httptest.NewRecorder()
		broker.Proxy(response, httptest.NewRequest("POST", target, strings.NewReader("{}")), "instance:1", instancebridge.Identity{UserID: "cp-member", Role: "owner"})
		if response.Code != 403 {
			t.Errorf("remote credential route %s status %d", target, response.Code)
		}
	}
	if _, err = manager.Configure(context.Background(), instanceconnection.Setup{ControlPlaneURL: cp.URL, RemoteAccess: false}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for (!manager.Status().Connected || !broker.Connected("instance:1")) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !manager.Status().Connected || !broker.Connected("instance:1") {
		t.Fatal("replacement connection did not establish")
	}
	// Node network operations still reach their own local authentication path,
	// even while remote human access is disabled. Unregistered nodes get no access.
	network := httptest.NewRecorder()
	networkRequest := httptest.NewRequest("POST", "/api/v1/edge/nodes/unregistered/challenge", strings.NewReader(`{"publicKey":"invalid"}`))
	networkRequest.Header.Set("Authorization", "Bearer invalid-node-credential")
	broker.ProxyOperation(network, networkRequest, "instance:1", instancebridge.OperationNetwork, instancebridge.Identity{})
	if network.Code != http.StatusUnauthorized {
		t.Fatalf("node credentials bypassed or blocked before local auth: %d %s", network.Code, network.Body.String())
	}
	closedUser := httptest.NewRecorder()
	broker.Proxy(closedUser, httptest.NewRequest("GET", "/api/v1/auth/me", nil), "instance:1", instancebridge.Identity{UserID: "cp-member"})
	if closedUser.Code != http.StatusForbidden {
		t.Fatalf("remote user access survived disable: %d", closedUser.Code)
	}
	if err = manager.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	local := httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer local-owner-token")
	handler.ServeHTTP(local, req)
	if local.Code != 200 {
		t.Fatalf("local login affected by disconnect %d", local.Code)
	}
}
