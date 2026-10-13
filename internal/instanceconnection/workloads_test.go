package instanceconnection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/store"
)

func TestWorkloadProxyOnlyServesLocallyOwnedPublishedRoute(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer app-token" || r.Header.Get("Cookie") != "app_session=valid" {
			t.Error("application credentials were dropped")
		}
		if r.Header.Get("X-Forwarded-Host") != r.Host || r.Header.Get("X-Forwarded-Proto") != "https" || r.Header.Get("X-Dispatch-Workload-Host") != "" {
			t.Error("untrusted forwarding headers survived")
		}
		w.Header().Set("Set-Cookie", "app_session=new; Secure")
		_, _ = w.Write([]byte("application response"))
	}))
	defer target.Close()
	m := testManager(t, nil)
	scope := core.HostedRoutingScope{TenantID: "instance", TenantSlug: "customer", RootDomain: "doodship.example"}
	app := core.App{ID: "app", ProjectID: "project", ServerID: "worker", State: "active"}
	host, err := routing.HostedHostname(scope, app)
	if err != nil {
		t.Fatal(err)
	}
	route := core.ApplicationRoute{AppID: app.ID, ProjectID: app.ProjectID, ServerID: app.ServerID, Hostname: host, DeploymentID: "deployment", Destination: target.URL}
	m.mu.Lock()
	m.current.Configured = true
	m.current.InstanceID = scope.TenantID
	m.current.InstanceSlug = scope.TenantSlug
	m.current.RootDomain = scope.RootDomain
	m.mu.Unlock()
	m.config.LookupApp = func(context.Context, string) (core.App, error) { return app, nil }
	m.config.ListRoutes = func(context.Context) ([]core.ApplicationRoute, error) {
		t.Fatal("public ingress scanned all application routes")
		return nil, nil
	}
	m.config.LookupRouteByHostname = func(_ context.Context, hostname string) (core.ApplicationRoute, error) {
		if hostname != route.Hostname {
			return core.ApplicationRoute{}, store.ErrNotFound
		}
		return route, nil
	}
	req := httptest.NewRequest("GET", "https://"+host+"/status", nil)
	req.Header.Set("Authorization", "Bearer app-token")
	req.Header.Set("Cookie", "app_session=valid")
	req.Header.Set("X-Dispatch-Workload-Host", "attacker.example")
	req.Header.Set("X-Forwarded-Proto", "http")
	response := httptest.NewRecorder()
	m.serveWorkload(response, req)
	if response.Code != 200 || response.Body.String() != "application response" || !strings.Contains(response.Header().Get("Set-Cookie"), "app_session=new") {
		t.Fatalf("proxy response %d %s", response.Code, response.Body.String())
	}
	for _, bad := range []struct {
		name   string
		mutate func()
	}{
		{"unpublished", func() { route.DeploymentID = "" }},
		{"different project", func() { route.DeploymentID = "deployment"; route.ProjectID = "other" }},
		{"different worker", func() { route.ProjectID = app.ProjectID; route.ServerID = "other" }},
		{"arbitrary host", func() { route.ServerID = app.ServerID; route.Hostname = "attacker.example" }},
	} {
		t.Run(bad.name, func(t *testing.T) {
			bad.mutate()
			w := httptest.NewRecorder()
			m.serveWorkload(w, req)
			if w.Code != 404 {
				t.Fatalf("unowned route served %d", w.Code)
			}
		})
	}
}

func TestRouteReconciliationReplacesChangedHostAndSkipsUnchangedRegistrations(t *testing.T) {
	m := testManager(t, nil)
	scope := core.HostedRoutingScope{TenantID: "instance", TenantSlug: "customer", RootDomain: "doodship.example"}
	apps := map[string]core.App{"changed": {ID: "changed", ProjectID: "project", ServerID: "worker"}, "stable": {ID: "stable", ProjectID: "project", ServerID: "worker"}}
	routes := []core.ApplicationRoute{}
	for _, id := range []string{"changed", "stable"} {
		host, err := routing.HostedHostname(scope, apps[id])
		if err != nil {
			t.Fatal(err)
		}
		routes = append(routes, core.ApplicationRoute{AppID: id, ProjectID: "project", ServerID: "worker", Hostname: host, RequestedDeploymentID: "deployment"})
	}
	events := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private" {
			t.Error("missing installation credential")
		}
		switch r.Method {
		case "GET":
			events <- "GET"
			_ = json.NewEncoder(w).Encode([]map[string]string{{"appID": "changed", "serverID": "worker", "hostname": "old.doodship.example"}, {"appID": "stable", "serverID": "worker", "hostname": routes[1].Hostname}})
		case "DELETE":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			events <- "DELETE:" + body["appID"].(string)
			w.WriteHeader(204)
		case "POST":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			events <- "POST:" + body["appID"].(string)
			_ = json.NewEncoder(w).Encode(map[string]string{"hostname": routes[0].Hostname})
		}
	}))
	defer server.Close()
	m.config.LookupApp = func(_ context.Context, id string) (core.App, error) { return apps[id], nil }
	m.config.ListRoutes = func(context.Context) ([]core.ApplicationRoute, error) { return routes, nil }
	m.config.LookupRoute = func(_ context.Context, id string) (core.ApplicationRoute, error) {
		for _, route := range routes {
			if route.AppID == id {
				return route, nil
			}
		}
		return core.ApplicationRoute{}, store.ErrNotFound
	}
	s := saved{Status: Status{Configured: true, ControlPlaneURL: server.URL, InstanceID: scope.TenantID}, InstanceSlug: scope.TenantSlug, RootDomain: scope.RootDomain, Credential: "private"}
	m.mu.Lock()
	m.current = s
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); m.reconcileRoutes(ctx, s) }()
	for _, expected := range []string{"GET", "DELETE:changed", "POST:changed"} {
		select {
		case event := <-events:
			if event != expected {
				t.Fatalf("route reconciliation order %q, wanted %q", event, expected)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("route reconciliation did not finish")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("route reconciliation did not stop")
	}
	select {
	case extra := <-events:
		t.Fatalf("unchanged route was rewritten: %s", extra)
	default:
	}
}
