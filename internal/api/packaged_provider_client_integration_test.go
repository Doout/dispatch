package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/automationclient"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provision"
	"github.com/oklog/ulid/v2"
)

// This fixture exercises a packaged mock over HTTP, not a real cloud provider.
func TestPackagedMockProviderCLIAndMCPAcceptanceIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_PROVIDER_CONTAINER_INTEGRATION") != "1" {
		t.Skip("set DISPATCH_PROVIDER_CONTAINER_INTEGRATION=1 for the packaged mock and shipped client")
	}
	cli := filepath.Join(t.TempDir(), "dispatchctl")
	buildContext, buildCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer buildCancel()
	build := exec.CommandContext(buildContext, "go", "build", "-o", cli, "./cmd/dispatchctl")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build supported client: %v %s", err, output)
	}
	buildCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	endpoint := packagedMockProvider(t, ctx)
	adapter, err := provider.NewClient(endpoint, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	a := serviceTestAPI(t)
	projects, err := a.store.ListProjects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatal("missing fixture project", err)
	}
	project := projects[0]
	var registered core.InfrastructureProvider
	raw := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/providers", provision.Registration{Name: "Packaged public mock", Endpoint: endpoint, Enabled: true, Capabilities: []string{provider.CapabilityCreate, provider.CapabilityInspect, provider.CapabilityDelete}}, 201)
	if err := json.Unmarshal(raw, &registered); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	keyID := "packaged-mock-ssh"
	if err := a.store.CreateSecret(ctx, core.Secret{ID: keyID, Name: "Fixture public SSH", Type: core.SecretTypeSSHPrivateKey, PublicValue: "ssh-ed25519 fixture", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for kind, id := range map[string]string{"provider": registered.ID, "ssh_key": keyID} {
		automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+project.ID, map[string]any{"kind": kind, "resourceId": id}, 200)
	}
	policy := core.InfrastructureQuotaPolicy{MaxServers: 1, Providers: []core.InfrastructureProviderRule{{ProviderID: registered.ID, Regions: []string{"mock-region"}, Sizes: []string{"mock-small"}}}}
	automationRequest(t, a, "secret", "PUT", "/api/v1/projects/"+project.ID+"/infrastructure/quota", policy, 200)
	permissions := []core.Permission{core.PermissionProjectView, core.PermissionInfrastructureInspect, core.PermissionInfrastructureCreate}
	token, grant := scopedServiceIdentity(t, a, project.ID, permissions...)
	credential := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(credential, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	controller := httptest.NewServer(a)
	defer controller.Close()
	manager := a.infrastructureManager()
	work, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-work.Done():
				done <- nil
				return
			case <-ticker.C:
				if _, err := manager.Reconcile(work); err != nil && work.Err() == nil {
					done <- err
					return
				}
			}
		}
	}()
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("packaged provider reconciliation: %v", err)
		}
	}()
	for _, transport := range []string{"cli", "mcp"} {
		t.Run(transport, func(t *testing.T) {
			grant.Permissions = append([]core.Permission(nil), permissions...)
			automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
			call := packagedCLI(t, ctx, cli, controller.URL, credential, token)
			if transport == "mcp" {
				call = packagedMCP(t, ctx, cli, controller.URL, credential, token)
			}
			must := func(name string, args automationclient.Arguments) automationclient.Result {
				t.Helper()
				result := call(name, args)
				if !result.OK {
					t.Fatalf("%s failed: %s", name, result.Error.Title)
				}
				return result
			}
			assertReplay := func(name string, args automationclient.Arguments, original core.MutationReceipt) {
				t.Helper()
				replay := must(name, args)
				var repeated core.MutationReceipt
				if err := json.Unmarshal(replay.Data, &repeated); err != nil || !replay.Replayed || repeated.ID != original.ID || repeated.OperationID != original.OperationID || repeated.ResourceID != original.ResourceID || replay.Continuation == nil || replay.Continuation.ID != original.ID || replay.Continuation.Key != args.Key {
					t.Fatal("same-key replay changed its accepted receipt or operation", err)
				}
			}
			must("providers_list", automationclient.Arguments{ProjectID: project.ID})
			foreign := core.Project{ID: "packaged-foreign-" + transport, Name: "Foreign " + transport, CreatedAt: now}
			if err := a.store.CreateProject(ctx, foreign); err != nil {
				t.Fatal(err)
			}
			foreignCatalog := must("providers_list", automationclient.Arguments{ProjectID: foreign.ID})
			var hidden []provision.CatalogProvider
			if err := json.Unmarshal(foreignCatalog.Data, &hidden); err != nil || len(hidden) != 0 {
				t.Fatal("inventory exposed another project's assigned providers", err)
			}
			if denied := call("provider_options", automationclient.Arguments{ProjectID: foreign.ID, ProviderID: registered.ID, Input: json.RawMessage(`{"kind":"sizes","config":{}}`)}); denied.Status != 403 || denied.ExitCode() != 3 {
				t.Fatal("client crossed the project grant")
			}
			input := automationclient.ServerCreateReview{ProjectID: project.ID, ProviderID: registered.ID, Name: "Packaged " + transport, Region: "mock-region", Size: "mock-small", Image: "mock-linux", Network: "mock-private", SSHKeySecretID: keyID, Config: map[string]any{}}
			body, _ := json.Marshal(input)
			result := must("server_review", automationclient.Arguments{Input: body})
			var review core.InfrastructureReview
			if err := json.Unmarshal(result.Data, &review); err != nil || review.ID == "" || review.Digest == "" {
				t.Fatal("missing immutable server review", err)
			}
			body, _ = json.Marshal(automationclient.InfrastructureAcceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: input.Name})
			create := automationclient.Arguments{Key: "packaged-create-" + transport, Input: body}
			accepted := must("server_create", create)
			var receipt core.MutationReceipt
			if err := json.Unmarshal(accepted.Data, &receipt); err != nil || receipt.ID == "" || receipt.OperationID == "" || accepted.Continuation == nil || accepted.Continuation.ID != receipt.ID || accepted.Continuation.Key != create.Key {
				t.Fatal("accepted creation lost its continuation", err)
			}
			assertReplay("server_create", create, receipt)
			must("receipt_wait", automationclient.Arguments{ReceiptID: receipt.ID, TimeoutSeconds: 20})
			data, err := manager.Store.(provision.LifecycleStore).GetManagedServer(ctx, review.ServerID)
			if err != nil || data.AllocationState != "allocated" || data.ResourceID == "" {
				t.Fatal("packaged provider allocation never completed", err)
			}
			resource, err := adapter.Server(ctx, data.ResourceID)
			if err != nil || resource.Labels["dispatch.server"] != data.ID || resource.Labels["dispatch.project"] != project.ID {
				t.Fatal("provider resource does not match accepted server", err)
			}
			operations := must("server_operations", automationclient.Arguments{ServerID: data.ID})
			var history []core.InfrastructureOperation
			if err := json.Unmarshal(operations.Data, &history); err != nil || len(history) != 1 || history[0].ID != receipt.OperationID || history[0].State != "succeeded" {
				t.Fatal("creation replay changed durable operation history", err)
			}
			if denied := call("server_delete_review", automationclient.Arguments{ServerID: data.ID}); denied.Status != 403 || denied.ExitCode() != 3 {
				t.Fatal("creation permission authorized deletion")
			}
			grant.Permissions = append(append([]core.Permission(nil), permissions...), core.PermissionInfrastructureDelete)
			automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
			result = must("server_delete_review", automationclient.Arguments{ServerID: data.ID})
			var deletion provision.DeletionReview
			if err := json.Unmarshal(result.Data, &deletion); err != nil || deletion.Server.ID != data.ID || deletion.Digest == "" {
				t.Fatal("deletion review lost the confirmed resource", err)
			}
			body, _ = json.Marshal(automationclient.InfrastructureAcceptance{Digest: deletion.Digest})
			if rejected := call("server_delete", automationclient.Arguments{ServerID: data.ID, Key: "packaged-unconfirmed-" + transport, Input: body}); rejected.OK || rejected.ExitCode() != 2 {
				t.Fatal("client manufactured a destructive confirmation")
			}
			body, _ = json.Marshal(automationclient.InfrastructureAcceptance{Digest: deletion.Digest, ConfirmName: "different server"})
			if rejected := call("server_delete", automationclient.Arguments{ServerID: data.ID, Key: "packaged-wrong-name-" + transport, Input: body}); rejected.OK || rejected.Status != 409 {
				t.Fatal("API accepted the wrong server confirmation")
			}
			if _, err := adapter.Server(ctx, data.ResourceID); err != nil {
				t.Fatal("rejected confirmation changed the provider resource", err)
			}
			body, _ = json.Marshal(automationclient.InfrastructureAcceptance{Digest: deletion.Digest, ConfirmName: deletion.Server.Name})
			remove := automationclient.Arguments{ServerID: data.ID, Key: "packaged-delete-" + transport, Input: body}
			removed := must("server_delete", remove)
			var cleanup core.MutationReceipt
			if err := json.Unmarshal(removed.Data, &cleanup); err != nil || cleanup.ID == "" || cleanup.OperationID == "" || cleanup.ID == receipt.ID || removed.Continuation == nil || removed.Continuation.Key != remove.Key {
				t.Fatal("deletion lost its distinct continuation", err)
			}
			assertReplay("server_delete", remove, cleanup)
			must("receipt_wait", automationclient.Arguments{ReceiptID: cleanup.ID, TimeoutSeconds: 20})
			if _, err := adapter.Server(ctx, data.ResourceID); err == nil {
				t.Fatal("controller success did not remove the packaged provider resource")
			} else {
				var problem *provider.Problem
				if !errors.As(err, &problem) || problem.Status != http.StatusNotFound {
					t.Fatal("unavailability was mistaken for provider-confirmed absence", err)
				}
			}
			operations = must("server_operations", automationclient.Arguments{ServerID: data.ID})
			if err := json.Unmarshal(operations.Data, &history); err != nil || len(history) != 2 {
				t.Fatal("cleanup did not preserve exactly two logical operations", err)
			}
			for _, operation := range history {
				if operation.State != "succeeded" || operation.ID != receipt.OperationID && operation.ID != cleanup.OperationID {
					t.Fatal("cleanup rewrote the original accepted operation")
				}
			}
			grant.Permissions = []core.Permission{core.PermissionProjectView}
			automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
			if denied := call("server_delete", remove); denied.Status != 403 {
				t.Fatal("receipt replay bypassed revoked deletion permission")
			}
		})
	}
}

func packagedMockProvider(t *testing.T, ctx context.Context) string {
	t.Helper()
	endpoint := os.Getenv("DOCKER_HOST")
	if endpoint == "" {
		out, err := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
		if err != nil {
			t.Fatal("inspect disposable Docker endpoint", err)
		}
		endpoint = strings.TrimSpace(string(out))
	}
	if !strings.HasPrefix(endpoint, "unix://") {
		t.Fatal("packaged provider fixture requires a local Docker Unix socket")
	}
	configuration := t.TempDir()
	docker := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(ctx, "docker", append([]string{"--config", configuration, "--host", endpoint}, args...)...)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("disposable Docker %s: %v %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	image := os.Getenv("DISPATCH_PROVIDER_IMAGE")
	if image == "" {
		image = "ghcr.io/doout/dispatch-provider-mock:main"
		docker("pull", image)
		t.Log("Public mock image pulled with an empty Docker configuration")
	} else {
		t.Log("Explicit provider image override; anonymous public pull is not established by this run")
	}
	imageID := docker("image", "inspect", "--format", "{{.Id}}", image)
	t.Logf("Packaged mock image %s; published digests %s", imageID, docker("image", "inspect", "--format", "{{json .RepoDigests}}", imageID))
	name := "dispatch-packaged-client-" + strings.ToLower(ulid.Make().String())
	volume := name + "-state"
	createdVolume, createdContainer := false, false
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, resource := range []struct {
			created bool
			args    []string
		}{{createdContainer, []string{"rm", "-f", "-v", name}}, {createdVolume, []string{"volume", "rm", volume}}} {
			if resource.created {
				if output, err := exec.CommandContext(cleanup, "docker", append([]string{"--host", endpoint}, resource.args...)...).CombinedOutput(); err != nil {
					t.Errorf("remove owned mock fixture: %v %s", err, output)
				}
			}
		}
	})
	docker("volume", "create", "--label", "dispatch.test=packaged-client", volume)
	createdVolume = true
	createdContainer = true
	docker("run", "-d", "--name", name, "--label", "dispatch.test=packaged-client", "--pull", "never", "--publish", "127.0.0.1::8091", "--mount", "type=volume,src="+volume+",dst=/data", imageID, "--listen", "0.0.0.0:8091", "--state", "/data/mock-state.json", "--polls", "1")
	address := docker("port", name, "8091/tcp")
	if !strings.HasPrefix(address, "127.0.0.1:") || strings.Contains(address, "\n") {
		t.Fatal("mock provider was not confined to one loopback port")
	}
	base := "http://" + address
	client, err := provider.NewClient(base, "", &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		manifest, err := client.Manifest(ctx)
		if err == nil && manifest.Name == "dispatch-mock" {
			return base
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("packaged mock did not become ready")
	return ""
}

type packagedCall func(string, automationclient.Arguments) automationclient.Result

func packagedCLI(t *testing.T, ctx context.Context, binary, endpoint, credential, token string) packagedCall {
	t.Helper()
	return func(operation string, args automationclient.Arguments) automationclient.Result {
		t.Helper()
		commandArgs := []string{"--url", endpoint, "--token-file", credential, operation}
		for _, field := range []struct{ flag, value string }{{"--project", args.ProjectID}, {"--provider", args.ProviderID}, {"--server", args.ServerID}, {"--receipt", args.ReceiptID}, {"--key", args.Key}} {
			if field.value != "" {
				commandArgs = append(commandArgs, field.flag, field.value)
			}
		}
		if args.TimeoutSeconds != 0 {
			commandArgs = append(commandArgs, "--timeout", strconv.Itoa(args.TimeoutSeconds))
		}
		if len(args.Input) != 0 {
			commandArgs = append(commandArgs, "--input", "-")
		}
		command := exec.CommandContext(ctx, binary, commandArgs...)
		command.Stdin = bytes.NewReader(args.Input)
		var output, diagnostics bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostics
		err := command.Run()
		var result automationclient.Result
		if json.Unmarshal(output.Bytes(), &result) != nil || strings.Contains(output.String(), token) || strings.Contains(diagnostics.String(), token) {
			t.Fatal("packaged CLI did not return sanitized client JSON")
		}
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != result.ExitCode() {
				t.Fatal("CLI exit status does not match its result")
			}
		} else if result.ExitCode() != 0 {
			t.Fatal("failed CLI result returned a successful exit")
		}
		return result
	}
}

func packagedMCP(t *testing.T, ctx context.Context, binary, endpoint, credential, token string) packagedCall {
	t.Helper()
	command := exec.CommandContext(ctx, binary, "--url", endpoint, "--token-file", credential, "mcp")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if err := command.Wait(); err != nil {
			t.Errorf("packaged MCP shutdown: %v", err)
		}
		if strings.Contains(diagnostics.String(), token) {
			t.Error("MCP diagnostics leaked the scoped credential")
		}
	})
	encoder := json.NewEncoder(stdin)
	reader := bufio.NewReader(stdout)
	read := func() map[string]json.RawMessage {
		t.Helper()
		line, err := reader.ReadBytes('\n')
		if err != nil || strings.Contains(string(line), token) {
			t.Fatal("missing or unsanitized MCP result")
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
	id := 1
	return func(operation string, args automationclient.Arguments) automationclient.Result {
		t.Helper()
		id++
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": operation, "arguments": args}}); err != nil {
			t.Fatal(err)
		}
		response := read()
		var payload struct {
			StructuredContent automationclient.Result `json:"structuredContent"`
		}
		if err := json.Unmarshal(response["result"], &payload); err != nil || payload.StructuredContent.Version != "dispatch.client/v1" {
			t.Fatal("MCP lost the typed client result", err)
		}
		return payload.StructuredContent
	}
}
