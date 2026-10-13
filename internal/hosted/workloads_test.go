package hosted

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/tenancy"
)

func hostedWorkloadFixture(t *testing.T) (*hostedFixture, tenancy.Tenant, core.ApplicationRoute) {
	t.Helper()
	f := newHostedFixture(t)
	tenant := f.tenant(t, "alpha", "owner-a")
	runtime, err := f.server.runtime(tenant)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	now := time.Now().UTC()
	if err := runtime.Store.CreateProject(ctx, core.Project{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	server := core.Server{ID: "server", Name: "Server", Runtime: core.ServerRuntimeDocker, State: "ready", CreatedAt: now, Routing: &core.RoutingConfig{BaseDomain: f.server.Config.RootDomain, Hosted: f.server.tenantAuthenticator(tenant).RoutingScope, RequireTLS: true}}
	if err := runtime.Store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	app := core.App{ID: "app", ProjectID: "project", ServerID: "server", Name: "Application", BuildType: core.BuildTypeDockerfile, ContainerPort: 8080, Generated: true, HelmProvenance: core.HelmProvenance{PullRequests: []core.HelmPullRequest{{Number: 223}}}, CreatedAt: now}
	if err := runtime.Store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	plan, err := routing.Plan(core.Deployment{ID: "deployment"}, app, server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Store.ReserveApplicationRoute(ctx, *plan); err != nil {
		t.Fatal(err)
	}
	if err := f.server.tenantAuthenticator(tenant).ReserveHostname(ctx, *plan); err != nil {
		t.Fatal(err)
	}
	return f, tenant, *plan
}

func TestWorkloadOwnershipAndPublishedRouteRequired(t *testing.T) {
	f, tenant, route := hostedWorkloadFixture(t)
	ctx := t.Context()
	runtime, _ := f.server.runtime(tenant)
	for _, host := range []string{route.Hostname, "p223-fake-alpha." + f.server.Config.RootDomain} {
		w := f.request(t, host, "GET", "/api/v1/private", "", nil)
		if w.Code != 404 {
			t.Fatal("unpublished workload reached controller", w.Code, w.Body.String())
		}
	}
	forged := route
	forged.Hostname = "app-other." + f.server.Config.RootDomain
	if err := f.server.tenantAuthenticator(tenant).ReserveHostname(ctx, forged); !errors.Is(err, tenancy.ErrDenied) {
		t.Fatal("accepted foreign ownership", err)
	}
	owner, err := f.catalog.WorkloadHost(ctx, route.Hostname)
	if err != nil {
		t.Fatal(err)
	}
	hijack := owner
	hijack.AppID = "other-app"
	if err := f.catalog.ReserveWorkloadHost(ctx, hijack); !errors.Is(err, tenancy.ErrDenied) {
		t.Fatal("hostname was reassigned", err)
	}
	hijack = owner
	hijack.Hostname = "app-alias." + f.server.Config.RootDomain
	if err := f.catalog.ReserveWorkloadHost(ctx, hijack); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatal("application claimed a second host", err)
	}
	route.DeploymentID = route.RequestedDeploymentID
	route.Destination = "http://127.0.0.1:3000"
	route.State = "published"
	if err := runtime.Store.SaveApplicationRoute(ctx, route); err != nil {
		t.Fatal(err)
	}
	f.server.Config.WorkloadGateway = ""
	if w := f.request(t, route.Hostname, "GET", "/", "", nil); w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := f.catalog.ReleaseWorkloadHost(ctx, "other-tenant", owner.AppID, owner.ServerID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.catalog.WorkloadHost(ctx, route.Hostname); err != nil {
		t.Fatal("foreign tenant released route", err)
	}
	if err := f.server.tenantAuthenticator(tenant).ReleaseHostname(ctx, owner.AppID, owner.ServerID); err != nil {
		t.Fatal(err)
	}
	if w := f.request(t, route.Hostname, "GET", "/", "", nil); w.Code != 404 {
		t.Fatal("released host remains routed", w.Code)
	}
}

func TestWorkloadProxyKeepsConsoleSessionsOffGateway(t *testing.T) {
	f, tenant, route := hostedWorkloadFixture(t)
	ctx := t.Context()
	runtime, _ := f.server.runtime(tenant)
	route.DeploymentID = route.RequestedDeploymentID
	route.Destination = "http://127.0.0.1:3000"
	route.State = "published"
	if err := runtime.Store.SaveApplicationRoute(ctx, route); err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != route.Hostname || r.Header.Get("X-Forwarded-Host") != route.Hostname || r.Header.Get("X-Forwarded-Proto") != "https" {
			t.Error("proxy lost registered host")
		}
		if strings.Contains(r.Header.Get("Cookie"), "dispatch") || !strings.Contains(r.Header.Get("Cookie"), "app=session") {
			t.Error("console session reached workload")
		}
		w.Header().Set("X-Dispatch-Deployment", route.DeploymentID)
		_, _ = w.Write([]byte("live workload"))
	}))
	defer gateway.Close()
	f.server.Config.WorkloadGateway = gateway.URL
	f.server.workloadTransport = gateway.Client().Transport
	r := httptest.NewRequest("GET", "https://"+route.Hostname+"/api/v1/private", nil)
	r.AddCookie(&http.Cookie{Name: tenantCookie, Value: "tenant-dispatch-session"})
	r.AddCookie(&http.Cookie{Name: platformCookie, Value: "platform-dispatch-session"})
	r.AddCookie(&http.Cookie{Name: "app", Value: "session"})
	r.Header.Set("X-Forwarded-Host", "bravo."+f.server.Config.RootDomain)
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "live workload" {
		t.Fatal(w.Code, w.Body.String())
	}
	f.server.cacheCertificate(platformOwner, hostedCertificateFixture(t, f.server.Config.RootDomain, "*."+f.server.Config.RootDomain))
	cert, err := tlsFixtureHandshake(t, f.server, route.Hostname)
	if err != nil || cert.VerifyHostname(route.Hostname) != nil {
		t.Fatal("registered workload did not receive shared certificate", err)
	}
	// A route left in the catalog cannot expose an application after cleanup.
	if err := runtime.Store.DeleteApplicationRoute(context.Background(), route.AppID, route.ServerID); err != nil {
		t.Fatal(err)
	}
	if w := f.request(t, route.Hostname, "GET", "/", "", nil); w.Code != 404 {
		t.Fatal("stale ownership reached gateway", w.Code)
	}
}
