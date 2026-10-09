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
	"github.com/doout/dispatch/internal/workflowrunner"
)

type hostedStoreFixture struct {
	app        core.App
	server     core.Server
	deployment core.Deployment
	outputs    map[string]string
	snapshot   core.DeploymentSnapshot
}

func (s *hostedStoreFixture) GetApp(context.Context, string) (core.App, error) { return s.app, nil }
func (s *hostedStoreFixture) GetServer(context.Context, string) (core.Server, error) {
	return s.server, nil
}
func (s *hostedStoreFixture) GetDeployment(context.Context, string) (core.Deployment, error) {
	return s.deployment, nil
}
func (s *hostedStoreFixture) UpdateDeploymentOutputs(_ context.Context, _ string, v map[string]string) error {
	s.outputs = v
	return nil
}
func (s *hostedStoreFixture) UpdateDeploymentSnapshot(_ context.Context, _ string, v core.DeploymentSnapshot) error {
	s.snapshot = v
	return nil
}

type hostedRunnerFunc func(context.Context, workflowrunner.Request, func(string)) (workflowrunner.Result, error)

func (f hostedRunnerFunc) Run(ctx context.Context, r workflowrunner.Request, p func(string)) (workflowrunner.Result, error) {
	return f(ctx, r, p)
}

func TestHostedDeploymentSendsPrivateInputsWithoutRunningHooks(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "hook-must-not-run")
	app := core.App{ID: "app", ProjectID: "project", ServerID: "target", Name: "App", BuildType: core.BuildTypeCompose, ComposeContent: "services: {}", PreDeployHook: "touch " + marker, SourceCredential: "repository-token", HelmValues: "replicas: 1", HelmGroupValues: "preview: true", ServiceRuntime: []core.ServiceRuntimeBinding{{SensitiveValues: []string{"service-password"}}}}
	server := core.Server{ID: "target", Runtime: core.ServerRuntimeDocker, AgentNodeID: "tenant-node"}
	d := core.Deployment{ID: "deployment", AppID: app.ID, CommitSHA: "commit", SpecDigest: app.SpecDigest(), State: core.DeploymentFetching, Snapshot: core.DeploymentSnapshot{TargetID: "target"}}
	data := &hostedStoreFixture{app: app, server: server, deployment: d}
	e := &HostedExecutor{Store: data, Authorize: func(context.Context, core.Deployment, core.App, core.Server) error { return nil }}
	var captured workflowrunner.Request
	var logs string
	e.Runner = hostedRunnerFunc(func(_ context.Context, request workflowrunner.Request, progress func(string)) (workflowrunner.Result, error) {
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &captured); err != nil {
			t.Fatal(err)
		}
		progress("repository-token service-password")
		return workflowrunner.Result{State: "succeeded", Outputs: map[string]string{"value": "service-password"}, Snapshot: &core.DeploymentSnapshot{TargetID: "target", Runtime: core.ServerRuntimeDocker}}, nil
	})
	if err := e.Deploy(context.Background(), d, app, server, func(_ core.DeploymentState, message string) error { logs += message; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("controller ran a tenant hook")
	}
	if captured.Deployment.SourceCredential != app.SourceCredential || captured.Deployment.ComposeContent != app.ComposeContent || captured.Deployment.HelmValues != app.HelmValues || captured.Deployment.HelmGroupValues != app.HelmGroupValues || len(captured.Deployment.ServiceRuntime) != 1 {
		t.Fatal("private execution inputs did not survive transport")
	}
	if strings.Contains(logs, "repository-token") || strings.Contains(logs, "service-password") || data.outputs["value"] != "[REDACTED]" {
		t.Fatal("worker result exposed supplied credentials")
	}
	if err := e.AuthorizeRequest(context.Background(), captured); err != nil {
		t.Fatal(err)
	}
	data.app.Branch = "changed"
	if e.AuthorizeRequest(context.Background(), captured) == nil {
		t.Fatal("changed application remained authorized")
	}
	data.app = app
	data.server.AgentNodeID = "other-node"
	if e.AuthorizeRequest(context.Background(), captured) == nil {
		t.Fatal("changed target remained authorized")
	}
	data.server = server
	data.deployment.State = core.DeploymentCancelled
	if e.AuthorizeRequest(context.Background(), captured) == nil {
		t.Fatal("cancelled acceptance remained authorized")
	}
}

func TestHostedDeploymentFailsClosedWhenWorkerOrAuthorizationMissing(t *testing.T) {
	app := core.App{ID: "app", ProjectID: "project", ServerID: "target", BuildType: core.BuildTypeDockerfile}
	server := core.Server{ID: "target", Runtime: core.ServerRuntimeDocker, AgentNodeID: "node"}
	for _, e := range []*HostedExecutor{{}, {Runner: hostedRunnerFunc(func(context.Context, workflowrunner.Request, func(string)) (workflowrunner.Result, error) {
		t.Fatal("unauthorized runner called")
		return workflowrunner.Result{}, nil
	})}} {
		if e.Deploy(context.Background(), core.Deployment{ID: "deployment", AppID: "app"}, app, server, func(core.DeploymentState, string) error { t.Fatal("local progress should not start"); return nil }) == nil {
			t.Fatal("unconfigured remote execution accepted")
		}
	}
}

func TestHostedDeploymentRejectsAlteredExecutionInputs(t *testing.T) {
	app := core.App{ID: "app", ProjectID: "project", ServerID: "target", Name: "App", BuildType: core.BuildTypeHelm, HelmValues: "replicas: 1", HelmNamespace: "team", PreDeployHook: "echo accepted", HookEnvironment: map[string]string{"PLAIN": "accepted"}, HelmProvenance: core.HelmProvenance{Sources: map[string]core.WorkflowSourceRevision{}}}
	server := core.Server{ID: "target", Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{Context: "team", Namespace: "team", KubeconfigData: "private-kubeconfig", CertificateAuthorityData: "private-ca"}}
	d := core.Deployment{ID: "run", AppID: app.ID, CommitSHA: "commit", SpecDigest: app.SpecDigest(), State: core.DeploymentFetching}
	data := &hostedStoreFixture{app: app, server: server, deployment: d}
	e := &HostedExecutor{Store: data, Authorize: func(context.Context, core.Deployment, core.App, core.Server) error { return nil }}
	var encoded []byte
	e.Runner = hostedRunnerFunc(func(_ context.Context, request workflowrunner.Request, _ func(string)) (workflowrunner.Result, error) {
		var err error
		encoded, err = json.Marshal(request)
		return workflowrunner.Result{State: "succeeded"}, err
	})
	if err := e.Deploy(context.Background(), d, app, server, nil); err != nil {
		t.Fatal(err)
	}
	var original workflowrunner.Request
	if err := json.Unmarshal(encoded, &original); err != nil {
		t.Fatal(err)
	}
	if err := e.AuthorizeRequest(context.Background(), original); err != nil {
		t.Fatal("serialized accepted inputs were refused", err)
	}
	for name, change := range map[string]func(*workflowrunner.Deployment){
		"hook":                  func(d *workflowrunner.Deployment) { d.App.PreDeployHook = "echo unapproved" },
		"values":                func(d *workflowrunner.Deployment) { d.HelmValues = "replicas: 100" },
		"generated values":      func(d *workflowrunner.Deployment) { d.HelmGeneratedValues = "replicas: 100" },
		"namespace":             func(d *workflowrunner.Deployment) { d.Server.Kubernetes.Namespace = "another-tenant" },
		"credential":            func(d *workflowrunner.Deployment) { d.Kubeconfig = "another-target" },
		"certificate authority": func(d *workflowrunner.Deployment) { d.CertificateAuthority = "another-ca" },
		"environment":           func(d *workflowrunner.Deployment) { d.HookEnvironment["PLAIN"] = "unapproved" },
		"provenance":            func(d *workflowrunner.Deployment) { d.HelmProvenance.WorkflowRevisionID = "another-run" },
	} {
		t.Run(name, func(t *testing.T) {
			var changed workflowrunner.Request
			if err := json.Unmarshal(encoded, &changed); err != nil {
				t.Fatal(err)
			}
			change(changed.Deployment)
			if err := e.AuthorizeRequest(context.Background(), changed); err == nil {
				t.Fatal("altered worker inputs kept a valid stored digest")
			}
		})
	}
}

func TestHostedCleanupPreservesAcceptedOperationAfterPreviewCloses(t *testing.T) {
	app := core.App{ID: "app", ProjectID: "project", ServerID: "target", Name: "Closed preview", State: "closing", BuildType: core.BuildTypeHelm, HelmProvenance: core.HelmProvenance{WorkflowResourceID: "expired-preview"}}
	server := core.Server{ID: "target", Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{KubeconfigData: "private-kubeconfig"}}
	data := &hostedStoreFixture{app: app, server: server}
	checked := 0
	e := &HostedExecutor{Store: data, Authorize: func(context.Context, core.Deployment, core.App, core.Server) error {
		t.Fatal("cleanup used deployment source trust")
		return nil
	}, AuthorizeCleanup: func(_ context.Context, operation string, _ core.App, _ core.Server) error {
		checked++
		if operation != "accepted-preview-cleanup" {
			t.Fatal("cleanup operation changed", operation)
		}
		return nil
	}}
	var requests []workflowrunner.Request
	e.Runner = hostedRunnerFunc(func(ctx context.Context, request workflowrunner.Request, _ func(string)) (workflowrunner.Result, error) {
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		var received workflowrunner.Request
		if err := json.Unmarshal(raw, &received); err != nil {
			t.Fatal(err)
		}
		if err := e.AuthorizeRequest(ctx, received); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, received)
		return workflowrunner.Result{State: "succeeded"}, nil
	})
	ctx := WithCleanupOperation(context.Background(), "accepted-preview-cleanup")
	for range 2 {
		if err := e.Cleanup(ctx, app, server, nil); err != nil {
			t.Fatal(err)
		}
	}
	if checked != 4 || requests[0].RevisionID != requests[1].RevisionID || requests[0].Deployment.Operation != "destroy" {
		t.Fatal("cleanup retries did not preserve accepted identity")
	}
	e.AuthorizeCleanup = nil
	if e.Cleanup(ctx, app, server, nil) == nil {
		t.Fatal("cleanup without a separate authorizer succeeded")
	}
}
