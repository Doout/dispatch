package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/automationclient"
	"github.com/doout/dispatch/internal/bootstrap"
	"github.com/doout/dispatch/internal/core"
	"golang.org/x/crypto/ssh"
)

// Exercise the shipped executable against authenticated API and durable state.
// The installer callback interrupts work without contacting an SSH host.
func TestTargetBootstrapExecutableCLIAndMCPScopedRecoveryIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "dispatchctl")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/dispatchctl")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build supported client: %v %s", err, output)
	}
	for _, transport := range []string{"cli", "mcp"} {
		t.Run(transport, func(t *testing.T) {
			a := serviceTestAPI(t)
			a.auth.PublicURL = "https://dispatch.example.com"
			artifactRoot := t.TempDir()
			t.Setenv("DISPATCH_EDGE_BINARY_ROOT", artifactRoot)
			if err := os.WriteFile(filepath.Join(artifactRoot, "linux-amd64"), []byte("reviewed agent artifact"), 0600); err != nil {
				t.Fatal(err)
			}
			projects, err := a.store.ListProjects(ctx)
			if err != nil || len(projects) == 0 {
				t.Fatal("missing fixture project")
			}
			project := projects[0]
			target := core.Server{ID: "bootstrap-client-target", ProjectID: project.ID, Name: "Recovery target", Address: "192.0.2.10", Runtime: core.ServerRuntimeDocker, AgentNodeID: "node-bootstrap-client-target", State: "ready", CreatedAt: time.Now()}
			if err := a.store.CreateServer(ctx, target); err != nil {
				t.Fatal(err)
			}
			key, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(make([]byte, 32)).Public())
			if err != nil {
				t.Fatal(err)
			}
			plan := core.TargetBootstrapPlan{Method: "ssh", Platform: "linux-amd64", ImageFamily: "existing-systemd", SSHHost: target.Address, SSHUser: "root", SSHVerified: true, SSHHostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}
			const password = "private-bootstrap-client-password"
			raw := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/bootstrap/review", map[string]any{"serverId": target.ID, "plan": plan, "credentials": map[string]string{"password": password}}, 201)
			var original core.TargetBootstrap
			if err := json.Unmarshal(raw, &original); err != nil {
				t.Fatal(err)
			}
			serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/bootstrap/"+original.ID+"/accept", map[string]string{"digest": original.Digest, "confirmName": target.Name}, 202)
			manager := a.bootstrapManager()
			installs := 0
			manager.SSHInstall = func(_ context.Context, item core.TargetBootstrap, credentials bootstrap.SSHCredentials, _ string) error {
				installs++
				if item.ID != original.ID || item.ServerID != target.ID || item.Digest != original.Digest || item.Plan.ArtifactSHA256 != original.Plan.ArtifactSHA256 || credentials.Password != password {
					t.Error("retry replaced the accepted installation or private inputs")
				}
				return errors.New("interrupted installation fixture")
			}
			if _, err := manager.InstallSSH(ctx, original.ID, original.Digest); err == nil {
				t.Fatal("interruption fixture unexpectedly succeeded")
			}
			token, grant := scopedServiceIdentity(t, a, project.ID, core.PermissionInfrastructureInspect)
			credential := filepath.Join(t.TempDir(), "credential")
			if err := os.WriteFile(credential, []byte(token), 0600); err != nil {
				t.Fatal(err)
			}
			controller := httptest.NewServer(a)
			defer controller.Close()
			call := bootstrapExecutableCLI(t, ctx, binary, controller.URL, credential, token)
			if transport == "mcp" {
				call = packagedMCP(t, ctx, binary, controller.URL, credential, token)
			}
			must := func(name string, args automationclient.Arguments) automationclient.Result {
				t.Helper()
				out := call(name, args)
				if !out.OK || strings.Contains(string(out.Data), password) || strings.Contains(string(out.Data), "encryptedInput") || strings.Contains(string(out.Data), "claimToken") {
					t.Fatalf("%s failed or returned private installer data", name)
				}
				return out
			}
			listed := must("bootstraps_list", automationclient.Arguments{ProjectID: project.ID, ServerID: target.ID})
			var items []core.TargetBootstrap
			if json.Unmarshal(listed.Data, &items) != nil || len(items) != 1 || items[0].ID != original.ID {
				t.Fatal("scoped inventory lost the original installation")
			}
			inspected := must("bootstrap_get", automationclient.Arguments{BootstrapID: original.ID})
			if inspected.Continuation == nil || inspected.Continuation.Kind != "target_bootstrap" || inspected.Continuation.ID != original.ID || inspected.Continuation.ResourceID != target.ID || inspected.Continuation.ProjectID != project.ID || installs != 1 {
				t.Fatal("inspection changed work or lost its continuation")
			}
			input, _ := json.Marshal(automationclient.BootstrapRetryInput{Digest: original.Digest})
			args := automationclient.Arguments{BootstrapID: original.ID, Input: input}
			if denied := call("bootstrap_retry", args); denied.Status != 403 || denied.ExitCode() != 3 || installs != 1 {
				t.Fatal("inspection permission authorized installation recovery")
			}
			grant.Permissions = append(grant.Permissions, core.PermissionInfrastructureModify)
			automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
			retried := must("bootstrap_retry", args)
			var item core.TargetBootstrap
			if json.Unmarshal(retried.Data, &item) != nil || item.ID != original.ID || item.Digest != original.Digest || item.State != "accepted" || retried.Continuation == nil || retried.Continuation.ID != original.ID || retried.Continuation.ResourceID != target.ID || installs != 1 {
				t.Fatal("explicit retry lost the original receipt or executed inline")
			}
			if _, err := manager.InstallSSH(ctx, original.ID, original.Digest); err == nil || installs != 2 {
				t.Fatal("retry did not execute the original accepted installer")
			}
			before, err := manager.Store.GetTargetBootstrap(ctx, original.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Store.RevokeEdgeCredential(ctx, original.NodeID, time.Now()); err != nil {
				t.Fatal(err)
			}
			if denied := call("bootstrap_retry", args); denied.Status != 409 || denied.Continuation == nil || denied.Continuation.ID != original.ID {
				t.Fatal("changed enrollment generation resumed installation")
			}
			after, err := manager.Store.GetTargetBootstrap(ctx, original.ID)
			if err != nil || before.Revision != after.Revision || installs != 2 {
				t.Fatal("rejected recovery changed durable installation state")
			}
			grant.Permissions = []core.Permission{core.PermissionProjectView}
			automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
			if denied := call("bootstrap_get", automationclient.Arguments{BootstrapID: original.ID}); denied.Status != 403 {
				t.Fatal("inspection bypassed withdrawn access")
			}
			if denied := call("bootstrap_retry", args); denied.Status != 403 || installs != 2 {
				t.Fatal("recovery bypassed withdrawn access")
			}
			listed = must("bootstraps_list", automationclient.Arguments{ProjectID: project.ID, ServerID: target.ID})
			if json.Unmarshal(listed.Data, &items) != nil || len(items) != 0 {
				t.Fatal("inventory retained an installation after access withdrawal")
			}
		})
	}
}

func bootstrapExecutableCLI(t *testing.T, ctx context.Context, binary, endpoint, credential, token string) packagedCall {
	t.Helper()
	return func(operation string, args automationclient.Arguments) automationclient.Result {
		t.Helper()
		arguments := []string{"--url", endpoint, "--token-file", credential, operation}
		for _, field := range []struct{ flag, value string }{{"--bootstrap", args.BootstrapID}, {"--project", args.ProjectID}, {"--server", args.ServerID}} {
			if field.value != "" {
				arguments = append(arguments, field.flag, field.value)
			}
		}
		if len(args.Input) != 0 {
			arguments = append(arguments, "--input", "-")
		}
		command := exec.CommandContext(ctx, binary, arguments...)
		command.Stdin = bytes.NewReader(args.Input)
		var output, diagnostics bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostics
		err := command.Run()
		var result automationclient.Result
		if json.Unmarshal(output.Bytes(), &result) != nil || strings.Contains(output.String(), token) || strings.Contains(diagnostics.String(), token) {
			t.Fatal("CLI did not return sanitized client JSON")
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
