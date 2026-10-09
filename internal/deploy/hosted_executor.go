package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/doout/dispatch/internal/workflowrunner"
	"github.com/oklog/ulid/v2"
)

type HostedDeploymentStore interface {
	GetApp(context.Context, string) (core.App, error)
	GetServer(context.Context, string) (core.Server, error)
	GetDeployment(context.Context, string) (core.Deployment, error)
	UpdateDeploymentOutputs(context.Context, string, map[string]string) error
	UpdateDeploymentSnapshot(context.Context, string, core.DeploymentSnapshot) error
}

// HostedExecutor never starts a process. SourceAuthExecutor may wrap it to
// resolve repository credentials; hooks and runtime drivers live on the worker.
type HostedExecutor struct {
	Runner             workflowrunner.Runner
	Store              HostedDeploymentStore
	Resolver           SecretResolver
	Vault              *secretcrypto.Vault
	Authorize          func(context.Context, core.Deployment, core.App, core.Server) error
	AuthorizeCleanup   func(context.Context, string, core.App, core.Server) error
	AuthorizeProvision func(context.Context, core.ServiceProvisionRequest, core.HelmServiceProvision, core.Server) error
	AuthorizeService   func(context.Context, workflowrunner.ServiceProvision, core.Server) error
	Capture            func(context.Context, core.Deployment, core.App, core.Server, string) error
}

func (e *HostedExecutor) RuntimeCapabilities(app core.App, server core.Server) runtimecontract.Manifest {
	ops := []runtimecontract.Operation{}
	supported := app.BuildType == core.BuildTypeHelm && core.IsKubernetesRuntime(server.Runtime) && server.Kubernetes != nil && server.Kubernetes.KubeconfigData != "" ||
		(app.BuildType == core.BuildTypeDockerfile || app.BuildType == core.BuildTypeCompose) && server.Runtime == core.ServerRuntimeDocker && server.AgentNodeID != "" && server.Routing == nil
	if e.Runner != nil && e.Store != nil && supported {
		if e.Authorize != nil {
			ops = append(ops, runtimecontract.Deploy)
		}
		if e.AuthorizeCleanup != nil {
			ops = append(ops, runtimecontract.Destroy)
		}
	}
	return runtimecontract.Describe("hosted-worker", "outbound", ops...)
}

func hostedInputServer(server core.Server, kubeconfig, ca string) core.Server {
	if server.Kubernetes != nil {
		config := *server.Kubernetes
		config.KubeconfigData, config.CertificateAuthorityData = kubeconfig, ca
		server.Kubernetes = &config
	}
	return server
}

func (e *HostedExecutor) hookEnvironment(ctx context.Context, app core.App) (map[string]string, error) {
	for key := range app.HookEnvironment {
		if _, _, secret := core.ParseSecretEnvironmentKey(key); secret && e.Resolver == nil && e.Vault == nil {
			return nil, errors.New("hook secret resolver is unavailable")
		}
	}
	resolved, err := resolveHookSecrets(ctx, app, e.Resolver, e.Vault)
	return resolved.HookEnvironment, err
}

func (e *HostedExecutor) Deploy(ctx context.Context, d core.Deployment, app core.App, server core.Server, progress Progress) error {
	return e.execute(ctx, "deploy", d, app, server, progress)
}
func (e *HostedExecutor) Cleanup(ctx context.Context, app core.App, server core.Server, progress Progress) error {
	return e.execute(ctx, "destroy", core.Deployment{}, app, server, progress)
}

func hostedServerDigest(server core.Server) string {
	var kubeconfig, ca string
	if server.Kubernetes != nil {
		kubeconfig = server.Kubernetes.KubeconfigData
		ca = server.Kubernetes.CertificateAuthorityData
	}
	raw, _ := json.Marshal(struct {
		Server         core.Server
		Kubeconfig, CA string
	}{server, kubeconfig, ca})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// AuthorizeRequest belongs on Broker.Authorize. The broker invokes it before
// submission and again while leasing and completing work, so queued credentials
// do not bypass a changed app, target, acceptance, or source-trust decision.
func (e *HostedExecutor) AuthorizeRequest(ctx context.Context, request workflowrunner.Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if p := request.ServiceProvision; p != nil {
		if e.Store == nil || e.AuthorizeService == nil && (p.Action() != "provision" || e.AuthorizeProvision == nil) {
			return errors.New("service provisioning authorization unavailable")
		}
		server, err := e.Store.GetServer(ctx, p.Server.ID)
		if err != nil {
			return err
		}
		if hostedServerDigest(server) != p.ServerDigest || hostedServerDigest(hostedInputServer(p.Server, p.Kubeconfig, p.CertificateAuthority)) != p.ServerDigest {
			return errors.New("service provisioning target changed")
		}
		if e.AuthorizeService != nil {
			return e.AuthorizeService(ctx, *p, server)
		}
		return e.AuthorizeProvision(ctx, p.Request, p.Spec, server)
	}
	op := request.Deployment
	if op == nil || e.Store == nil || op.Operation == "deploy" && e.Authorize == nil || op.Operation == "destroy" && e.AuthorizeCleanup == nil {
		return errors.New("deployment authorization unavailable")
	}
	app, err := e.Store.GetApp(ctx, op.App.ID)
	if err != nil {
		return err
	}
	if app.ProjectID != request.ProjectID || app.ServerID != op.Server.ID || app.Name != op.App.Name || app.Template != op.App.Template || app.Generated != op.App.Generated || app.SpecDigest() != op.AppSpecDigest {
		return errors.New("deployment application changed")
	}
	input := op.App
	input.ComposeContent, input.HelmValues, input.HelmGroupValues = op.ComposeContent, op.HelmValues, op.HelmGroupValues
	input.HookEnvironment = app.HookEnvironment
	submittedProvenance, _ := json.Marshal(op.HelmProvenance)
	storedProvenance, _ := json.Marshal(app.HelmProvenance)
	if input.SpecDigest() != op.AppSpecDigest || op.HelmGeneratedValues != app.HelmGeneratedValues || string(submittedProvenance) != string(storedProvenance) {
		return errors.New("deployment inputs do not match the accepted application")
	}
	if op.Operation == "deploy" {
		if len(app.HookEnvironment) != len(op.HookEnvironment) {
			return errors.New("deployment hook inputs changed")
		}
		for key, value := range app.HookEnvironment {
			_, variable, secret := core.ParseSecretEnvironmentKey(key)
			if secret {
				key = resolvedSecretPrefix + variable
			}
			provided, exists := op.HookEnvironment[key]
			if !exists || !secret && provided != value {
				return errors.New("deployment hook inputs changed")
			}
		}
	}
	server, err := e.Store.GetServer(ctx, app.ServerID)
	if err != nil {
		return err
	}
	if hostedServerDigest(server) != op.ServerDigest || hostedServerDigest(hostedInputServer(op.Server, op.Kubeconfig, op.CertificateAuthority)) != op.ServerDigest {
		return errors.New("deployment target changed")
	}
	if op.Operation == "destroy" {
		return e.AuthorizeCleanup(ctx, request.RevisionID, app, server)
	}
	deployment, err := e.Store.GetDeployment(ctx, request.RevisionID)
	if err != nil {
		return err
	}
	if deployment.AppID != app.ID || deployment.CommitSHA != op.Deployment.CommitSHA || deployment.SpecDigest != op.Deployment.SpecDigest || deployment.State.Terminal() {
		return errors.New("deployment acceptance changed")
	}
	return e.Authorize(ctx, deployment, app, server)
}

func (e *HostedExecutor) execute(ctx context.Context, operation string, d core.Deployment, app core.App, server core.Server, progress Progress) error {
	capability := runtimecontract.Deploy
	if operation == "destroy" {
		capability = runtimecontract.Destroy
	}
	if err := e.RuntimeCapabilities(app, server).Check(ctx, capability); err != nil {
		return err
	}
	revision := d.ID
	if operation == "destroy" {
		revision, _ = ctx.Value(cleanupOperationKey{}).(string)
		if revision == "" {
			revision = "cleanup-" + ulid.Make().String()
		}
	}
	op := &workflowrunner.Deployment{Operation: operation, Deployment: d, App: app, Server: server, AppSpecDigest: app.SpecDigest(), ServerDigest: hostedServerDigest(server), SourceCredential: app.SourceCredential, ComposeContent: app.ComposeContent, HelmValues: app.HelmValues, HelmGeneratedValues: app.HelmGeneratedValues, HelmGroupValues: app.HelmGroupValues, HelmProvenance: app.HelmProvenance, Snapshot: d.Snapshot, ServiceRuntime: app.ServiceRuntime}
	request := workflowrunner.Request{Version: workflowrunner.Version, ProjectID: app.ProjectID, ResourceID: app.ID, RevisionID: revision, Mode: "tenant", Deployment: op}
	if operation == "deploy" && len(app.HookEnvironment) > 0 {
		resolved, err := e.hookEnvironment(ctx, app)
		if err != nil {
			return err
		}
		op.HookEnvironment = resolved
	}
	if server.Kubernetes != nil {
		op.Kubeconfig = server.Kubernetes.KubeconfigData
		op.CertificateAuthority = server.Kubernetes.CertificateAuthorityData
	}
	if err := e.AuthorizeRequest(ctx, request); err != nil {
		return err
	}
	// Remove nested API conveniences; only the explicitly captured inputs cross
	// the worker boundary, including fields excluded from public JSON models.
	op.Deployment.App = nil
	op.Deployment.Server = nil
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var progressErr error
	previous := ""
	result, runErr := e.Runner.Run(ctx, request, func(log string) {
		if progressErr != nil || log == previous {
			return
		}
		message := log
		if strings.HasPrefix(log, previous) {
			message = strings.TrimPrefix(log, previous)
		}
		previous = log
		if message != "" && progress != nil {
			progressErr = progress(core.DeploymentBuilding, request.Redact(message))
			if progressErr != nil {
				cancel()
			}
		}
	})
	if progressErr != nil {
		return progressErr
	}
	if result.Health != nil {
		if err := reportDeploymentHealth(ctx, d, *result.Health); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	if result.Route != nil {
		return errors.Join(runErr, errors.New("hosted worker returned an unsupported route"))
	}
	if result.Snapshot != nil && operation == "deploy" {
		if result.Snapshot.TargetID != server.ID || result.Snapshot.Runtime != server.Runtime {
			return errors.Join(runErr, errors.New("worker snapshot target changed"))
		}
		if err := e.Store.UpdateDeploymentSnapshot(ctx, d.ID, *result.Snapshot); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	if len(result.Outputs) > 0 && operation == "deploy" {
		for key, value := range result.Outputs {
			result.Outputs[key] = request.Redact(value)
		}
		if err := e.Store.UpdateDeploymentOutputs(ctx, d.ID, result.Outputs); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	if result.Manifest != "" && e.Capture != nil && operation == "deploy" {
		if err := e.Capture(ctx, d, app, server, result.Manifest); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New(request.Redact(runErr.Error()))
	}
	if result.State != "succeeded" {
		message := result.Error
		if message == "" {
			message = "worker deployment did not succeed"
		}
		return errors.New(request.Redact(message))
	}
	return nil
}
