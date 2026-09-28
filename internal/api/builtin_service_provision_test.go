package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/serviceconn"
	"github.com/oklog/ulid/v2"
)

func TestBuiltinServiceTemplateTargetValidation(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	server := core.Server{ID: "provision-local", Name: "local", Runtime: "docker", Address: "local", CreatedAt: time.Now().UTC()}
	if err := a.store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	template := func(id string) string {
		return fmt.Sprintf("apiVersion: dispatch/v1alpha1\nkind: ServiceTemplate\nmetadata: {name: postgres}\nspec:\n  serviceType: postgresql\n  provision:\n    docker: {serverRef: %s}\n", id)
	}
	body := map[string]any{"projectId": projects[0].ID, "document": template("missing")}
	serviceRequestTest(t, a, "POST", "/api/v1/service-templates", body, 400)
	body["document"] = template(server.ID)
	response := serviceRequestTest(t, a, "POST", "/api/v1/service-templates", body, 201)
	var saved core.SavedServiceTemplate
	_ = json.Unmarshal(response, &saved)
	detail := serviceRequestTest(t, a, "GET", "/api/v1/service-templates/"+saved.ID, nil, 200)
	if !strings.Contains(string(detail), `"provider":"docker"`) || strings.Contains(saved.Document, "run:") {
		t.Fatal("not a declarative Docker template")
	}
	// A target can change after a template is saved. Reject it before starting a run.
	server.Runtime = "builder"
	if err := a.store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "POST", "/api/v1/service-templates/"+saved.ID+"/runs", map[string]any{"name": "orders"}, 400)
	runs, _ := a.store.ListServiceProvisionRuns(ctx, projects[0].ID)
	if len(runs) != 0 {
		t.Fatal("invalid target created a provision run")
	}
}

func TestBuiltinDockerServiceProvisionEndToEnd(t *testing.T) {
	if os.Getenv("DISPATCH_TEST_BUILTIN_DOCKER") != "1" {
		t.Skip("set DISPATCH_TEST_BUILTIN_DOCKER=1 to provision a disposable PostgreSQL container")
	}
	a := serviceTestAPI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	projects, _ := a.store.ListProjects(ctx)
	server := core.Server{ID: "provision-local", Name: "local", Runtime: "docker", Address: "local", CreatedAt: time.Now().UTC()}
	if err := a.store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	network := "dispatch-test-" + strings.ToLower(ulid.Make().String())
	var name string
	var app core.App
	t.Cleanup(func() {
		if app.ID != "" {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = (deploy.DockerExecutor{}).Cleanup(cleanup, app, server, func(core.DeploymentState, string) error { return nil })
		}
		if name != "" {
			exec.Command("docker", "rm", "-f", "-v", name).Run()
			exec.Command("docker", "volume", "rm", name+"-data").Run()
		}
		exec.Command("docker", "network", "rm", network).Run()
	})
	doc := fmt.Sprintf("apiVersion: dispatch/v1alpha1\nkind: ServiceTemplate\nmetadata: {name: postgres}\nspec:\n  serviceType: postgresql\n  provision:\n    docker: {serverRef: %s, network: %s}\n", server.ID, network)
	var saved core.SavedServiceTemplate
	_ = json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/service-templates", map[string]any{"projectId": projects[0].ID, "document": doc}, 201), &saved)
	var run core.ServiceProvisionRun
	_ = json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/service-templates/"+saved.ID+"/runs", map[string]any{"name": "orders"}, 202), &run)
	name = deploy.ServiceResourceName(run.ID)
	for run.State == "queued" || run.State == "running" {
		select {
		case <-ctx.Done():
			t.Fatal("provisioning did not finish")
		case <-time.After(100 * time.Millisecond):
		}
		data := serviceRequestTest(t, a, "GET", "/api/v1/service-provision-runs/"+run.ID, nil, 200)
		_ = json.Unmarshal(data, &run)
	}
	if run.Target == nil || run.Target.ResourceName != name {
		t.Fatal("missing provision run target")
	}
	if run.State != "succeeded" {
		t.Fatalf("provision failed: %s", run.Error)
	}
	item, err := a.store.GetService(ctx, run.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Fields["password"].EncryptedValue == "" || item.Fields["password"].Value != "" || item.ProvisionTarget == nil || item.ProvisionTarget.ResourceName != name {
		t.Fatal("missing encrypted credentials or resource provenance")
	}
	fields, err := (serviceconn.Resolver{Vault: a.eventConfig.Vault}).Resolve(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	// Deploy a bound application. Its network and credentials must be supplied
	// by Dispatch, without a network setting in the application definition.
	app = core.App{ID: "provision-consumer-" + strings.ToLower(ulid.Make().String()), ProjectID: projects[0].ID, ServerID: server.ID, Name: "provision-consumer", BuildType: core.BuildTypeCompose, CreatedAt: time.Now().UTC(), ComposeContent: "services:\n  consumer:\n    image: " + deploy.DefaultServiceImage + "\n    command: [sh, -c, 'psql \"$$DATABASE_URL\" -tAc \"SELECT 1\" > /tmp/connected; sleep 300']\n"}
	if err := a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	binding := []core.ServiceBinding{{Alias: "db", ServiceRef: item.ID, Compose: map[string]map[string]string{"consumer": {"DATABASE_URL": "connectionUrl"}}}}
	serviceRequestTest(t, a, "PUT", "/api/v1/apps/"+app.ID+"/service-bindings", binding, 200)
	runner := deploy.NewService(a.store, deploy.DockerExecutor{})
	runner.ConfigureServices(a.eventConfig.Vault, nil)
	deployed, err := runner.Start(ctx, app.ID, "inline")
	if err != nil {
		t.Fatal(err)
	}
	for !deployed.State.Terminal() {
		select {
		case <-ctx.Done():
			_ = runner.Cancel(context.Background(), deployed.ID)
			t.Fatal("application deployment did not finish")
		case <-time.After(100 * time.Millisecond):
		}
		deployed, err = a.store.GetDeployment(ctx, deployed.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if deployed.State != core.DeploymentSucceeded {
		t.Fatalf("bound application deployment failed: %s", deployed.Message)
	}
	consumer := "dispatch-" + app.ID + "-consumer-1"
	connected := false
	for attempt := 0; attempt < 30; attempt++ {
		output, err := exec.CommandContext(ctx, "docker", "exec", consumer, "cat", "/tmp/connected").CombinedOutput()
		if err == nil && strings.TrimSpace(string(output)) == "1" {
			connected = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !connected {
		t.Fatal("bound application failed to connect with automatically attached service network and credentials")
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/apps/"+app.ID+"/service-bindings", []core.ServiceBinding{}, 200)
	response := serviceRequestTest(t, a, "GET", "/api/v1/services", nil, 200)
	if strings.Contains(string(response), fields["password"]) {
		t.Fatal("service response exposed credentials")
	}
	runs := serviceRequestTest(t, a, "GET", "/api/v1/service-provision-runs", nil, 200)
	if strings.Contains(string(runs), fields["password"]) {
		t.Fatal("run history exposed credentials")
	}
	inspect, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .HostConfig.PortBindings}}", name).Output()
	if err != nil || strings.TrimSpace(string(inspect)) != "{}" {
		t.Fatal("database published a host port")
	}
	// Removing the registration must not erase the database volume.
	serviceRequestTest(t, a, "DELETE", "/api/v1/services/"+item.ID, nil, 204)
	if err := exec.CommandContext(ctx, "docker", "volume", "inspect", name+"-data").Run(); err != nil {
		t.Fatal("removing the registration erased persistent storage")
	}
}
