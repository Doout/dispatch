package routing

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

func TestHostedPreviewNamesShareOneWildcardWithoutCollisions(t *testing.T) {
	scope := core.HostedRoutingScope{TenantID: "tenant", TenantSlug: "agentops", RootDomain: "doodship.com"}
	seen := map[string]bool{}
	for i := 0; i < 5000; i++ {
		app := core.App{ID: "app-" + strconv.Itoa(i), ProjectID: "project", Generated: true, HelmProvenance: core.HelmProvenance{PullRequests: []core.HelmPullRequest{{Number: 223}}}}
		host, err := HostedHostname(scope, app)
		if err != nil || seen[host] || !strings.HasPrefix(host, "p223-") || !strings.HasSuffix(host, "-agentops.doodship.com") || strings.Count(host, ".") != 2 {
			t.Fatal(host, err)
		}
		seen[host] = true
	}
	app := core.App{ID: "same-app", ProjectID: "project", Generated: true, HelmProvenance: core.HelmProvenance{PullRequests: []core.HelmPullRequest{{Number: 223}}}}
	preview, _ := HostedHostname(scope, app)
	if parts := strings.Split(strings.TrimSuffix(preview, ".doodship.com"), "-"); len(parts) != 3 || parts[0] != "p223" || len(parts[1]) != 16 || parts[2] != "agentops" {
		t.Fatal("preview must put its ID before the tenant name", preview)
	}
	app.Generated = false
	first, _ := HostedHostname(scope, app)
	if !strings.HasPrefix(first, "app-") || !strings.HasSuffix(first, "-agentops.doodship.com") {
		t.Fatal("application must put its ID before the tenant name", first)
	}
	scope.TenantID = "other-tenant"
	other, _ := HostedHostname(scope, app)
	if first == other {
		t.Fatal("tenant IDs did not affect route ownership")
	}
	app.Generated = true
	scope.TenantSlug = strings.Repeat("a", 63)
	scope.RootDomain = "local.doodship.com"
	host, err := HostedHostname(scope, app)
	if err != nil || len(strings.Split(host, ".")[0]) != 63 || !strings.HasSuffix(host, ".local.doodship.com") {
		t.Fatal(host, err)
	}
}

func TestHostedRouteRejectsCustomAndConsoleHostnames(t *testing.T) {
	scope := &core.HostedRoutingScope{TenantID: "tenant", TenantSlug: "agentops", RootDomain: "doodship.com"}
	app := core.App{ID: "app", ProjectID: "project", ServerID: "server", BuildType: core.BuildTypeDockerfile, ContainerPort: 8080}
	server := core.Server{ID: "server", Routing: &core.RoutingConfig{BaseDomain: scope.RootDomain, Hosted: scope, RequireTLS: true}}
	plan, err := Plan(core.Deployment{ID: "deploy"}, app, server)
	if err != nil || plan == nil || !plan.GatewayTLS || plan.EntryPoint != "web" {
		t.Fatal(plan, err)
	}
	for _, host := range []string{"agentops.doodship.com", "local.doodship.com", "pr-223-other.doodship.com", "pr-223.agentops.doodship.com", "custom.example.com"} {
		app.Domain = host
		if _, err := Plan(core.Deployment{ID: "deploy"}, app, server); err == nil {
			t.Fatal("accepted custom hostname", host)
		}
	}
	app.Domain = plan.Hostname
	if _, err := Plan(core.Deployment{ID: "redeploy"}, app, server); err != nil {
		t.Fatal(err)
	}
}

func TestHostedWorkerRouteDoesNotIssueWildcardCertificates(t *testing.T) {
	publisher, plan := routeFixture(t)
	plan.GatewayTLS = true
	plan.EntryPoint = "web"
	plan.TLSResolver = ""
	if _, err := publisher.Prepare(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Promote(t.Context(), plan, LoopbackDestination(32000)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(publisher.Directory, routeFile(plan.AppID)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "certResolver") || strings.Contains(string(raw), "tls:") {
		t.Fatal("worker received an ACME TLS route", string(raw))
	}
}
