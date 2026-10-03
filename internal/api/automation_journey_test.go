package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/automationclient"
	"github.com/doout/dispatch/internal/core"
)

// Exercise the shipped binary and its MCP transport against the API. Runtime
// deployment and service allocation are deterministic fixtures, not cloud proof.
func TestScopedAutomationCLIAndMCPJourney(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cli := filepath.Join(t.TempDir(), "dispatchctl")
	build := exec.CommandContext(ctx, "go", "build", "-o", cli, "./cmd/dispatchctl")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build supported client: %v %s", err, output)
	}
	a, runtime, template := resourceAPIFixture(t)
	servers, err := a.store.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var target core.Server
	for _, s := range servers {
		if s.Runtime == core.ServerRuntimeDocker && s.State == "ready" && s.ID != "resource-target" {
			target = s
			break
		}
	}
	if target.ID == "" {
		t.Fatal("missing ready simulation target")
	}
	token, grant := scopedServiceIdentity(t, a, template.ProjectID, core.PermissionProjectView, core.PermissionProjectConfigure, core.PermissionDeploymentRun, core.PermissionServiceProvision)
	for _, assignment := range []map[string]any{
		{"kind": "target", "resourceId": target.ID},
		{"kind": "target", "resourceId": "resource-target"},
		{"kind": "service_template", "resourceId": template.ID + "@" + template.Digest},
	} {
		automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+template.ProjectID, assignment, 200)
	}
	automationRequest(t, a, "secret", "PUT", "/api/v1/projects/"+template.ProjectID+"/infrastructure/quota", core.InfrastructureQuotaPolicy{MaxServices: 1, Providers: []core.InfrastructureProviderRule{}}, 200)
	credential := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(credential, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a)
	defer server.Close()
	prefix := []string{"--url", server.URL, "--token-file", credential}
	run := func(input string, args ...string) automationclient.Result {
		t.Helper()
		command := exec.CommandContext(ctx, cli, append(append([]string{}, prefix...), args...)...)
		command.Stdin = strings.NewReader(input)
		var output, diagnostics bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostics
		runErr := command.Run()
		var result automationclient.Result
		if err := json.Unmarshal(output.Bytes(), &result); err != nil || strings.Contains(output.String(), token) || strings.Contains(diagnostics.String(), token) {
			t.Fatalf("client did not return sanitized JSON for %v", args)
		}
		if runErr != nil {
			exit, ok := runErr.(*exec.ExitError)
			if !ok || exit.ExitCode() != result.ExitCode() {
				t.Fatalf("exit contract changed for %v: %v", args, runErr)
			}
		} else if result.ExitCode() != 0 {
			t.Fatal("failure returned successful exit", args)
		}
		return result
	}
	must := func(result automationclient.Result) automationclient.Result {
		t.Helper()
		if !result.OK {
			t.Fatalf("automation failed: %s", result.Error.Title)
		}
		return result
	}
	input, _ := json.Marshal(automationclient.ApplicationInput{ProjectID: template.ProjectID, ServerID: target.ID, Name: "agent-created-api", ComposeContent: "services:\n  web:\n    image: busybox:1.37\n"})
	created := must(run(string(input), "app", "create", "--key", "agent-application-once", "--input", "-"))
	appID := created.Continuation.ResourceID
	if appID == "" || created.Continuation.OperationID != appID {
		t.Fatal("application acceptance lost identity")
	}
	if replay := must(run(string(input), "app", "create", "--key", "agent-application-once", "--input", "-")); !replay.Replayed || replay.Continuation.ID != created.Continuation.ID {
		t.Fatal("CLI replay created another application")
	}
	must(run("", "app", "get", "--app", appID))
	must(run(`[]`, "app", "bindings", "set", "--app", appID, "--input", "-"))
	must(run(`{"timeoutSeconds":20,"checks":[]}`, "app", "health", "set", "--app", appID, "--input", "-"))
	preview := must(run("", "deployment", "preview", "--app", appID, "--revision", "inline"))
	var plan struct {
		Review core.DeploymentReview `json:"review"`
	}
	if err := json.Unmarshal(preview.Data, &plan); err != nil {
		t.Fatal(err)
	}
	deployInput, _ := json.Marshal(automationclient.DeploymentStart{CommitSHA: "inline", Review: &plan.Review})
	accepted := must(run(string(deployInput), "deployment", "start", "--app", appID, "--key", "agent-deployment-once", "--input", "-"))
	if accepted.Continuation == nil {
		t.Fatal("deployment lost receipt")
	}
	must(run("", "receipt", "wait", "--receipt", accepted.Continuation.ID, "--timeout", "10"))
	if replay := must(run(string(deployInput), "deployment", "start", "--app", appID, "--key", "agent-deployment-once", "--input", "-")); !replay.Replayed {
		t.Fatal("CLI replay duplicated deployment")
	}
	var deploymentReceipt core.MutationReceipt
	if err := json.Unmarshal(accepted.Data, &deploymentReceipt); err != nil {
		t.Fatal(err)
	}
	must(run("", "deployment", "logs", "--deployment", deploymentReceipt.OperationID, "--limit", "2"))
	must(run("", "app", "deployments", "--app", appID))
	if denied := run("", "deployment", "cancel", "--deployment", deploymentReceipt.OperationID); denied.Status != 403 {
		t.Fatal("run permission granted cancellation")
	}
	must(run("", "service", "templates", "list", "--project", template.ProjectID))
	must(run("", "service", "template", "get", "--template", template.ID))
	serviceInput := `{"name":"agent-database","inputs":{}}`
	serviceAccepted := must(run(serviceInput, "service", "provision", "--template", template.ID, "--key", "agent-service-once", "--input", "-"))
	var serviceReceipt core.MutationReceipt
	if err := json.Unmarshal(serviceAccepted.Data, &serviceReceipt); err != nil {
		t.Fatal(err)
	}
	awaitServiceResource(t, a, serviceReceipt.OperationID, "ready")
	must(run("", "receipt", "wait", "--receipt", serviceReceipt.ID, "--timeout", "10"))
	must(run("", "service", "run", "get", "--run", serviceReceipt.OperationID))
	must(run("", "service", "get", "--run", serviceReceipt.OperationID))
	if replay := must(run(serviceInput, "service", "provision", "--template", template.ID, "--key", "agent-service-once", "--input", "-")); !replay.Replayed {
		t.Fatal("CLI service replay duplicated resource")
	}
	runtime.mu.Lock()
	createdServices := runtime.created
	runtime.mu.Unlock()
	if createdServices != 1 {
		t.Fatal("multiple service resources created", createdServices)
	}
	foreignProject := core.Project{ID: "foreign-agent-project", Name: "Foreign", CreatedAt: time.Now().UTC()}
	if err := a.store.CreateProject(ctx, foreignProject); err != nil {
		t.Fatal(err)
	}
	foreignApp := core.App{ID: "foreign-agent-app", Name: "Foreign", ProjectID: foreignProject.ID, ServerID: target.ID, State: "ready", BuildType: core.BuildTypeCompose, CreatedAt: time.Now().UTC()}
	if err := a.store.CreateApp(ctx, foreignApp); err != nil {
		t.Fatal(err)
	}
	if denied := run("", "app", "get", "--app", foreignApp.ID); denied.Status != 403 {
		t.Fatal("CLI read foreign app")
	}

	// Keep stdin open until each tool completes; closing MCP cancels outstanding
	// waits only and must not be used as an implicit server mutation.
	mcp := exec.CommandContext(ctx, cli, append(append([]string{}, prefix...), "mcp")...)
	stdin, err := mcp.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := mcp.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var mcpDiagnostics bytes.Buffer
	mcp.Stderr = &mcpDiagnostics
	if err := mcp.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if mcp.ProcessState == nil {
			_ = mcp.Process.Kill()
			_ = mcp.Wait()
		}
	})
	encoder := json.NewEncoder(stdin)
	reader := bufio.NewReader(stdout)
	read := func() map[string]json.RawMessage {
		t.Helper()
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("missing MCP result: %v", err)
		}
		if strings.Contains(string(line), token) {
			t.Fatal("MCP leaked credential")
		}
		var value map[string]json.RawMessage
		if json.Unmarshal(line, &value) != nil {
			t.Fatal("MCP returned non-protocol output")
		}
		return value
	}
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25"}}); err != nil {
		t.Fatal(err)
	}
	if read()["result"] == nil {
		t.Fatal("MCP initialization failed")
	}
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		t.Fatal(err)
	}
	tool := func(id int, name string, args map[string]any) automationclient.Result {
		t.Helper()
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}); err != nil {
			t.Fatal(err)
		}
		response := read()
		var payload struct {
			StructuredContent automationclient.Result `json:"structuredContent"`
		}
		if err := json.Unmarshal(response["result"], &payload); err != nil {
			t.Fatal("missing typed MCP result", err)
		}
		return payload.StructuredContent
	}
	var serviceBody map[string]any
	json.Unmarshal([]byte(serviceInput), &serviceBody)
	mcpReplay := must(tool(2, "service_provision", map[string]any{"templateId": template.ID, "key": "agent-service-once", "input": serviceBody}))
	if !mcpReplay.Replayed || mcpReplay.Continuation.ID != serviceReceipt.ID {
		t.Fatal("MCP did not reuse CLI acceptance")
	}
	must(tool(3, "app_get", map[string]any{"appId": appID}))
	if denied := tool(4, "app_get", map[string]any{"appId": foreignApp.ID}); denied.Status != 403 {
		t.Fatal("MCP read foreign app")
	}
	grant.Permissions = []core.Permission{core.PermissionProjectView}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	if denied := tool(5, "service_provision", map[string]any{"templateId": template.ID, "key": "agent-service-once", "input": serviceBody}); denied.Status != 403 {
		t.Fatal("MCP replay bypassed revoked provision permission")
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mcp.Wait(); err != nil {
		t.Fatalf("MCP shutdown: %v", err)
	}
	if strings.Contains(mcpDiagnostics.String(), token) {
		t.Fatal("MCP diagnostics leaked token")
	}
}
