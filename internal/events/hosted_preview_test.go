package events

import (
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
)

func TestHostedPreviewReportsGeneratedAddressInPR(t *testing.T) {
	data, template := previewFixture(t)
	ctx := t.Context()
	server, err := data.GetServer(ctx, template.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	server.Routing = &core.RoutingConfig{BaseDomain: "doodship.com", Hosted: &core.HostedRoutingScope{TenantID: "tenant", TenantSlug: "alpha", RootDomain: "doodship.com"}}
	if err := data.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	template.ContainerPort = 8080
	template.Domain = ""
	if err := data.UpdateApp(ctx, template); err != nil {
		t.Fatal(err)
	}
	deployments := deploy.NewService(data, deploy.SimulationExecutor{Delay: time.Millisecond})
	done := make(chan struct{})
	deployments.OnFinished = func(core.Deployment) { close(done) }
	lifecycle := DeploymentLifecycle{Store: data, Deployments: deployments}
	result, err := lifecycle.StartPreview(ctx, core.PreviewEnvironment{ID: "preview", TemplateAppID: template.ID, Repository: "acme/checkout", PullRequestNumber: 223, HeadSHA: strings.Repeat("a", 40)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.URL, "https://p223-") || !strings.HasSuffix(result.URL, "-alpha.doodship.com") {
		t.Fatal("PR has no flat preview link", result.URL)
	}
	// Join the asynchronous executor before its test database is closed.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("preview deployment did not finish")
	}
	app, err := data.GetApp(ctx, result.AppID)
	if err != nil || "https://"+app.Domain != result.URL {
		t.Fatal(app, err)
	}
}
