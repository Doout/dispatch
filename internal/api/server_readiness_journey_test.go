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
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
	"github.com/doout/dispatch/internal/provision"
	"github.com/doout/dispatch/internal/store"
)

// This journey uses the shipped CLI and MCP transports with actual project
// authorization and durable mock allocation. It does not boot a cloud machine.
func TestManagedServerCLIAndMCPReadinessJourney(t *testing.T) {
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelBuild()
	cli := filepath.Join(t.TempDir(), "dispatchctl")
	command := exec.CommandContext(buildCtx, "go", "build", "-o", cli, "./cmd/dispatchctl")
	command.Dir = "../.."
	if raw, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build supported client: %v %s", err, raw)
	}
	cancelBuild()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	a := serviceTestAPI(t)
	projects, err := a.store.ListProjects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatal("missing project", err)
	}
	project := projects[0]
	adapter, err := mock.New(mock.Options{})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(provider.Handler(adapter, ""))
	defer upstream.Close()
	p, err := a.infrastructureManager().Register(ctx, provision.Registration{Name: "Readiness fixture", Endpoint: upstream.URL, Enabled: true, Capabilities: []string{provider.CapabilityCreate, provider.CapabilityInspect, provider.CapabilityDelete}})
	if err != nil {
		t.Fatal(err)
	}
	token, grant := scopedServiceIdentity(t, a, project.ID, core.PermissionProjectView, core.PermissionInfrastructureInspect, core.PermissionInfrastructureCreate)
	if err = a.store.CreateSecret(ctx, core.Secret{ID: "readiness-key", Name: "Public SSH", Type: core.SecretTypeSSHPrivateKey, PublicValue: "ssh-ed25519 fixture", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for kind, id := range map[string]string{"provider": p.ID, "ssh_key": "readiness-key"} {
		automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+project.ID, map[string]any{"kind": kind, "resourceId": id}, 200)
	}
	automationRequest(t, a, "secret", "PUT", "/api/v1/projects/"+project.ID+"/infrastructure/quota", core.InfrastructureQuotaPolicy{MaxServers: 1, Providers: []core.InfrastructureProviderRule{{ProviderID: p.ID, Regions: []string{"mock-region"}, Sizes: []string{"mock-small"}}}}, 200)
	credential := filepath.Join(t.TempDir(), "token")
	if err = os.WriteFile(credential, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	controller := httptest.NewServer(a)
	defer controller.Close()
	prefix := []string{"--url", controller.URL, "--token-file", credential}
	run := func(input string, args ...string) automationclient.Result {
		t.Helper()
		command := exec.CommandContext(ctx, cli, append(append([]string{}, prefix...), args...)...)
		command.Stdin = strings.NewReader(input)
		var output, diagnostics bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostics
		err := command.Run()
		var result automationclient.Result
		if json.Unmarshal(output.Bytes(), &result) != nil || strings.Contains(output.String(), token) || strings.Contains(diagnostics.String(), token) {
			t.Fatal("client did not return sanitized JSON", args)
		}
		if err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != result.ExitCode() {
				t.Fatal("client exit contract changed", args, err)
			}
		} else if result.ExitCode() != 0 {
			t.Fatal("client failure returned successful exit", args)
		}
		return result
	}
	create, _ := json.Marshal(automationclient.ServerCreateReview{ProjectID: project.ID, ProviderID: p.ID, Name: "readiness-server", Region: "mock-region", Size: "mock-small", Image: "mock-linux", Network: "mock-private", SSHKeySecretID: "readiness-key", Config: map[string]any{}})
	reviewResult := run(string(create), "server", "review", "--input", "-")
	var review core.InfrastructureReview
	if !reviewResult.OK || json.Unmarshal(reviewResult.Data, &review) != nil {
		t.Fatal("creation review failed", reviewResult)
	}
	acceptance, _ := json.Marshal(automationclient.InfrastructureAcceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Name})
	accepted := run(string(acceptance), "server", "create", "--key", "readiness-machine-once", "--input", "-")
	var receipt core.MutationReceipt
	if !accepted.OK || json.Unmarshal(accepted.Data, &receipt) != nil {
		t.Fatal("creation acceptance failed", accepted)
	}
	manager := a.infrastructureManager()
	now := time.Now().UTC()
	manager.Now = func() time.Time { return now }
	for range 5 {
		now = now.Add(3 * time.Second)
		if _, err = manager.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		ops, e := manager.Store.(store.InfrastructureLifecycleStore).ListInfrastructureOperations(ctx, review.ServerID)
		if e != nil {
			t.Fatal(e)
		}
		if len(ops) == 1 && ops[0].State == "succeeded" {
			break
		}
	}
	manager.Now = nil
	allocated := run("", "receipt", "wait", "--receipt", receipt.ID, "--timeout", "5")
	if !allocated.OK {
		t.Fatal("mock allocation did not complete", allocated)
	}
	inspected := run("", "server", "get", "--server", review.ServerID)
	if !inspected.OK || inspected.Continuation == nil || inspected.Continuation.OperationID != receipt.OperationID {
		t.Fatal("inspection lost accepted identity", inspected)
	}
	waiting := run("", "server", "wait", "--server", review.ServerID, "--timeout", "1")
	if waiting.ExitCode() != 4 || waiting.Continuation.ID != review.ServerID || waiting.Continuation.OperationID != receipt.OperationID || !strings.Contains(string(waiting.Data), `"deployable":false`) {
		t.Fatal("allocation success was mistaken for runtime readiness", waiting)
	}
	// An owner issues enrollment explicitly. Read-only automation cannot obtain it.
	var enrollment provision.Enrollment
	raw := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers/"+review.ServerID+"/enrollment", nil, 201)
	if json.Unmarshal(raw, &enrollment) != nil {
		t.Fatal("invalid enrollment")
	}
	node, err := a.store.GetPrivateNetwork(ctx, enrollment.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	node.EnrollmentToken = enrollment.Token
	session := enrollNodeTest(t, a, node)
	runtimeNodeRequest(t, a, "GET", "/api/v1/edge/nodes/"+node.ID+"/runtime/jobs/next", session.Token, nil, 204)
	ready := run("", "server", "wait", "--server", review.ServerID, "--timeout", "5")
	if !ready.OK || !strings.Contains(string(ready.Data), `"deployable":true`) {
		t.Fatal("authenticated runtime did not become ready", ready)
	}

	mcp := exec.CommandContext(ctx, cli, append(append([]string{}, prefix...), "mcp")...)
	stdin, err := mcp.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := mcp.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	mcp.Stderr = &diagnostics
	if err = mcp.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if mcp.ProcessState == nil {
			_ = mcp.Process.Kill()
			_ = mcp.Wait()
		}
	})
	encoder, reader := json.NewEncoder(stdin), bufio.NewReader(stdout)
	read := func() map[string]json.RawMessage {
		t.Helper()
		line, err := reader.ReadBytes('\n')
		if err != nil || strings.Contains(string(line), token) || strings.Contains(string(line), enrollment.Token) {
			t.Fatal("MCP did not return sanitized protocol output", err)
		}
		var result map[string]json.RawMessage
		if json.Unmarshal(line, &result) != nil {
			t.Fatal("invalid MCP response")
		}
		return result
	}
	encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25"}})
	read()
	encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	tool := func(id int, name string) automationclient.Result {
		t.Helper()
		encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": map[string]any{"serverId": review.ServerID, "timeoutSeconds": 5}}})
		var payload struct {
			StructuredContent automationclient.Result `json:"structuredContent"`
		}
		if json.Unmarshal(read()["result"], &payload) != nil {
			t.Fatal("invalid structured MCP response")
		}
		return payload.StructuredContent
	}
	mcpReady := tool(2, "server_wait")
	if !mcpReady.OK || mcpReady.Continuation.ID != review.ServerID || mcpReady.Continuation.OperationID != receipt.OperationID {
		t.Fatalf("MCP disagreed with CLI readiness: %+v error=%+v continuation=%+v", mcpReady, mcpReady.Error, mcpReady.Continuation)
	}
	grant.Permissions = []core.Permission{core.PermissionProjectView}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	denied := tool(3, "server_wait")
	if denied.Status != 403 || denied.Continuation == nil || denied.Continuation.ID != review.ServerID {
		t.Fatal("MCP readiness ignored grant revocation", denied)
	}
	stdin.Close()
	if err := mcp.Wait(); err != nil || strings.Contains(diagnostics.String(), token) {
		t.Fatal("MCP shutdown failed", err)
	}
	ops, err := manager.Store.(store.InfrastructureLifecycleStore).ListInfrastructureOperations(ctx, review.ServerID)
	if err != nil || len(ops) != 1 || ops[0].ID != receipt.OperationID {
		t.Fatal("inspection or waiting created another provider operation", ops, err)
	}
}
