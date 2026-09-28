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
	t.Cleanup(func() {
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
	// A second container connects over TCP with the actual saved credentials.
	// Keep credentials out of process arguments and test output.
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", network, "-e", "PGHOST", "-e", "PGUSER", "-e", "PGPASSWORD", "-e", "PGDATABASE", deploy.DefaultServiceImage, "psql", "-tAc", "SELECT 1")
	cmd.Env = append(os.Environ(), "PGHOST="+fields["host"], "PGUSER="+fields["username"], "PGPASSWORD="+fields["password"], "PGDATABASE="+fields["database"])
	output, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "1" {
		t.Fatal("consumer container failed to connect using saved service fields")
	}
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
