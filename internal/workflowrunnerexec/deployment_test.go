package workflowrunnerexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/workflowrunner"
)

type deploymentRuntimeFixture struct {
	seen    *core.App
	cleaned *bool
}

func (f deploymentRuntimeFixture) Deploy(_ context.Context, _ core.Deployment, app core.App, server core.Server, p deploy.Progress) error {
	*f.seen = app
	if server.AgentNodeID != "" || server.Address != "local" {
		panic("worker target not mapped to its daemon")
	}
	return p(core.DeploymentStarting, "Started fixture")
}
func (f deploymentRuntimeFixture) Cleanup(context.Context, core.App, core.Server, deploy.Progress) error {
	*f.cleaned = true
	return nil
}

func deploymentRequest() workflowrunner.Request {
	app := core.App{ID: "app", ProjectID: "project", ServerID: "server", Name: "app", BuildType: core.BuildTypeCompose}
	return workflowrunner.Request{Version: workflowrunner.Version, ProjectID: "project", ResourceID: "app", RevisionID: "deployment", Mode: "tenant", Deployment: &workflowrunner.Deployment{Operation: "deploy", App: app, Server: core.Server{ID: "server", Runtime: core.ServerRuntimeDocker, AgentNodeID: "worker"}, Deployment: core.Deployment{ID: "deployment", AppID: "app"}, ComposeContent: "services: {}", HelmValues: "replicas: 2"}}
}

func privateWorkspace(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDeploymentWorkerRunsHooksAndReturnsEvidence(t *testing.T) {
	r := deploymentRequest()
	r.Deployment.App.PreDeployHook = `printf '{"built":"worker"}' > "$DISPATCH_OUTPUT_FILE"`
	r.Deployment.App.PostDeployHook = `test "$DISPATCH_OUTPUT_BUILT" = worker`
	var seen core.App
	cleaned := false
	result := executeDeploymentUsing(context.Background(), r, privateWorkspace(t), nil, func(*workflowrunner.Result) deploy.Executor {
		return deploymentRuntimeFixture{seen: &seen, cleaned: &cleaned}
	})
	if result.State != "succeeded" {
		t.Fatal(result.Error)
	}
	if result.Outputs["built"] != "worker" || result.Snapshot == nil || result.Snapshot.TargetID != "server" || seen.ComposeContent != "services: {}" || seen.HelmValues != "replicas: 2" {
		t.Fatal("worker hooks, inputs, or snapshot missing")
	}
	if !strings.Contains(result.Log, "Started fixture") {
		t.Fatal("runtime progress not returned")
	}
	r.Deployment.Operation = "destroy"
	result = executeDeploymentUsing(context.Background(), r, privateWorkspace(t), nil, func(*workflowrunner.Result) deploy.Executor {
		return deploymentRuntimeFixture{seen: &seen, cleaned: &cleaned}
	})
	if result.State != "succeeded" || !cleaned {
		t.Fatal("worker cleanup did not execute")
	}
}

func TestDeploymentWorkerRejectsHostFilesAndUnresolvedSecrets(t *testing.T) {
	for _, mutate := range []func(*workflowrunner.Request){
		func(r *workflowrunner.Request) { r.Deployment.App.SourceRepo = "file:///controller/private" },
		func(r *workflowrunner.Request) { r.Deployment.Server.AgentNodeID = "" },
		func(r *workflowrunner.Request) {
			r.Deployment.Server.Routing = &core.RoutingConfig{BaseDomain: "apps.example.test"}
		},
		func(r *workflowrunner.Request) { r.Mode = "managed" },
	} {
		r := deploymentRequest()
		mutate(&r)
		result := executeDeploymentUsing(context.Background(), r, privateWorkspace(t), nil, func(*workflowrunner.Result) deploy.Executor { t.Fatal("invalid input reached runtime"); return nil })
		if result.State != "failed" {
			t.Fatal("invalid worker input accepted")
		}
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0600)
	r := deploymentRequest()
	result := executeDeploymentUsing(context.Background(), r, filepath.Join(root, "file"), nil, func(*workflowrunner.Result) deploy.Executor { t.Fatal("invalid workspace reached runtime"); return nil })
	if result.State != "failed" {
		t.Fatal("non-directory workspace accepted")
	}
}
