package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
)

type outputRecorder struct{ outputs map[string]string }

func (r *outputRecorder) UpdateDeploymentOutputs(_ context.Context, _ string, outputs map[string]string) error {
	r.outputs = outputs
	return nil
}

func TestHookExecutorDecryptsOnlyBoundSecretForHookProcess(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := vault.Encrypt("secret:registry", []byte("super-secret-token"))
	if err != nil {
		t.Fatal(err)
	}
	var captured []string
	executor := HookExecutor{Next: &captureExecutor{}, Vault: vault, checkout: func(_ context.Context, _ core.Deployment, app core.App, _ string) error {
		if token := hookGitToken(app); token != "super-secret-token" {
			t.Fatalf("checkout did not receive attached token: %q", token)
		}
		return nil
	}, runHook: func(_ context.Context, _ string, _ string, environment []string) error {
		captured = append([]string(nil), environment...)
		return nil
	}}
	app := core.App{SourceRepo: "https://github.com/acme/private.git", PreDeployHook: "docker login", HookEnvironment: map[string]string{
		core.SecretEnvironmentKey("registry", "GITHUB_TOKEN"): ciphertext,
	}}
	if err := executor.Deploy(context.Background(), core.Deployment{}, app, core.Server{}, func(core.DeploymentState, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(captured, "\n")
	if !strings.Contains(joined, "GITHUB_TOKEN=super-secret-token") {
		t.Fatalf("decrypted secret was not provided: %#v", captured)
	}
	if strings.Contains(joined, ciphertext) {
		t.Fatal("encrypted storage value leaked into the hook environment")
	}
}

func TestHookErrorsRedactAttachedSecretValues(t *testing.T) {
	err := redactHookError(errors.New("login failed for super-secret-token"), core.App{HookEnvironment: map[string]string{
		resolvedSecretPrefix + "REGISTRY_TOKEN": "super-secret-token",
	}})
	if strings.Contains(err.Error(), "super-secret-token") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("secret was not redacted: %v", err)
	}
}

type captureExecutor struct {
	app       core.App
	called    bool
	cleanedUp bool
}

func (e *captureExecutor) Deploy(_ context.Context, _ core.Deployment, app core.App, _ core.Server, progress Progress) error {
	e.app, e.called = app, true
	return progress(core.DeploymentFetching, "resolving deployment source")
}

func (e *captureExecutor) Cleanup(_ context.Context, _ core.App, _ core.Server, progress Progress) error {
	e.cleanedUp = true
	return progress(core.DeploymentSucceeded, "removed")
}

func TestHookExecutorRunsHooksAndPassesGeneratedValuesSeparately(t *testing.T) {
	next := &captureExecutor{}
	order := []string{}
	executor := HookExecutor{Next: next, runHook: func(_ context.Context, script, workspace string, environment []string) error {
		order = append(order, script)
		if script == "pre" {
			for _, entry := range environment {
				if path, found := strings.CutPrefix(entry, "DISPATCH_VALUES_FILE="); found {
					return os.WriteFile(filepath.Clean(path), []byte("image:\n  tag: preview"), 0o600)
				}
			}
			t.Fatal("values file environment was not provided")
		}
		if workspace == "" {
			t.Fatal("hook workspace was not provided")
		}
		return nil
	}}
	app := core.App{ID: "app-1", Name: "Preview", BuildType: core.BuildTypeHelm, HelmValues: "replicas: 1", PreDeployHook: "pre", PostDeployHook: "post"}
	states := []core.DeploymentState{}
	err := executor.Deploy(context.Background(), core.Deployment{CommitSHA: "abc123"}, app, core.Server{Name: "cluster"}, func(state core.DeploymentState, _ string) error {
		states = append(states, state)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !next.called || next.app.HelmValues != "replicas: 1" || !strings.Contains(next.app.HelmGeneratedValues, "tag: preview") {
		t.Fatalf("generated values were not passed to the deployment: %#v", next.app)
	}
	for index := 1; index < len(states); index++ {
		if states[index] == core.DeploymentFetching && states[index-1] == core.DeploymentBuilding {
			t.Fatalf("deployment progress regressed: %#v", states)
		}
	}
	if strings.Join(order, ",") != "pre,post" {
		t.Fatalf("unexpected hook order: %v", order)
	}
}

func TestHookEnvironmentExcludesControllerSecretsAndIncludesExplicitHookVariables(t *testing.T) {
	t.Setenv("PATH", "/usr/local/bin:/usr/bin")
	t.Setenv("DISPATCH_ADMIN_PASSWORD", "controller-secret")
	t.Setenv("DISPATCH_GITHUB_WEBHOOK_SECRET", "webhook-secret")
	t.Setenv("HELM_REPOSITORY_PASSWORD", "repository-secret")
	t.Setenv("DISPATCH_HOOK_REGISTRY_TOKEN", "hook-token")
	environment := hookEnvironment(core.Deployment{CommitSHA: "abc123"}, core.App{ID: "app-1", Name: "Preview", HookEnvironment: map[string]string{
		"DISPATCH_EVENT_REPOSITORY": "acme/app", "DISPATCH_COMPONENT_API_URL": "https://api.example.test", "UNSAFE_SECRET": "blocked",
	}}, core.Server{Name: "cluster"}, "/tmp/values.yaml", "/tmp/outputs.json")
	joined := "\n" + strings.Join(environment, "\n") + "\n"
	for _, secret := range []string{"DISPATCH_ADMIN_PASSWORD=", "DISPATCH_GITHUB_WEBHOOK_SECRET=", "HELM_REPOSITORY_PASSWORD="} {
		if strings.Contains(joined, "\n"+secret) {
			t.Fatalf("controller secret %q was exposed to the hook", secret)
		}
	}
	for _, allowed := range []string{"PATH=/usr/local/bin:/usr/bin", "DISPATCH_HOOK_REGISTRY_TOKEN=hook-token", "DISPATCH_EVENT_REPOSITORY=acme/app", "DISPATCH_COMPONENT_API_URL=https://api.example.test", "DISPATCH_APP_ID=app-1", "DISPATCH_VALUES_FILE=/tmp/values.yaml"} {
		if !strings.Contains(joined, "\n"+allowed+"\n") {
			t.Fatalf("expected hook variable %q in %#v", allowed, environment)
		}
	}
	if strings.Contains(joined, "UNSAFE_SECRET=") {
		t.Fatalf("unexpected arbitrary hook variable in %#v", environment)
	}
}

func TestHookExecutorRollsBackSuccessfulDeploymentWhenPostHookFails(t *testing.T) {
	next := &captureExecutor{}
	executor := HookExecutor{Next: next, runHook: func(_ context.Context, script, _ string, _ []string) error {
		if script == "post" {
			return errors.New("publish failed")
		}
		return nil
	}}
	err := executor.Deploy(context.Background(), core.Deployment{}, core.App{PostDeployHook: "post"}, core.Server{}, func(core.DeploymentState, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "post-deploy hook") || !strings.Contains(err.Error(), "deployment rolled back") {
		t.Fatalf("expected post-hook error, got %v", err)
	}
	if !next.called || !next.cleanedUp {
		t.Fatalf("expected successful deployment to be rolled back: %#v", next)
	}
}

func TestHookExecutorPersistsValidatedPostHookOutputs(t *testing.T) {
	next, recorder := &captureExecutor{}, &outputRecorder{}
	executor := HookExecutor{Next: next, Outputs: recorder, runHook: func(_ context.Context, script, _ string, environment []string) error {
		if script != "post" {
			return nil
		}
		for _, entry := range environment {
			if path, ok := strings.CutPrefix(entry, "DISPATCH_OUTPUT_FILE="); ok {
				contents, _ := json.Marshal(map[string]string{"url": "https://preview.example.test", "apiToken": "ready"})
				return os.WriteFile(filepath.Clean(path), contents, 0o600)
			}
		}
		return errors.New("output path missing")
	}}
	if err := executor.Deploy(context.Background(), core.Deployment{ID: "deployment"}, core.App{PostDeployHook: "post"}, core.Server{}, func(core.DeploymentState, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if recorder.outputs["url"] != "https://preview.example.test" || recorder.outputs["apiToken"] != "ready" {
		t.Fatalf("outputs not persisted: %#v", recorder.outputs)
	}
}

func TestHookExecutorRejectsImmutableAndOversizedOutputs(t *testing.T) {
	for name, contents := range map[string]string{
		"immutable": `{"namespace":"other"}`,
		"oversized": `{"value":"` + strings.Repeat("x", 65*1024) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			next := &captureExecutor{}
			executor := HookExecutor{Next: next, runHook: func(_ context.Context, _ string, _ string, environment []string) error {
				for _, entry := range environment {
					if path, ok := strings.CutPrefix(entry, "DISPATCH_OUTPUT_FILE="); ok {
						return os.WriteFile(filepath.Clean(path), []byte(contents), 0o600)
					}
				}
				return nil
			}}
			err := executor.Deploy(context.Background(), core.Deployment{}, core.App{PostDeployHook: "post"}, core.Server{}, func(core.DeploymentState, string) error { return nil })
			if err == nil || !strings.Contains(err.Error(), "post-deploy outputs") || !next.cleanedUp {
				t.Fatalf("expected rejected output and rollback, got %v %#v", err, next)
			}
		})
	}
}
