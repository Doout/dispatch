package workflowrunnerexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/kubeconfig"
	"github.com/doout/dispatch/internal/workflowrunner"
	"k8s.io/client-go/tools/clientcmd"
)

type deploymentEvidence struct {
	result  *workflowrunner.Result
	request workflowrunner.Request
}

func (e deploymentEvidence) UpdateDeploymentOutputs(_ context.Context, _ string, outputs map[string]string) error {
	if e.result.Outputs == nil {
		e.result.Outputs = map[string]string{}
	}
	for key, value := range outputs {
		e.result.Outputs[key] = e.request.Redact(value)
	}
	return nil
}
func (e deploymentEvidence) UpdateDeploymentSnapshot(_ context.Context, _ string, snapshot core.DeploymentSnapshot) error {
	e.result.Snapshot = &snapshot
	return nil
}

func executeDeployment(ctx context.Context, request workflowrunner.Request, workspace string, progress func(string)) workflowrunner.Result {
	return executeDeploymentUsing(ctx, request, workspace, progress, func(result *workflowrunner.Result) deploy.Executor {
		return deploy.RuntimeExecutor{Default: deploy.DockerExecutor{}, Helm: deploy.HelmExecutor{Capture: func(_ context.Context, _ core.Deployment, _ core.App, _ core.Server, manifest string) error {
			if len(manifest) > 2<<20 {
				return errors.New("rendered manifest exceeds the worker result limit")
			}
			result.Manifest = manifest
			return nil
		}}}
	})
}

func executeDeploymentUsing(ctx context.Context, request workflowrunner.Request, workspace string, progress func(string), runtime func(*workflowrunner.Result) deploy.Executor) workflowrunner.Result {
	result := workflowrunner.Result{State: "failed"}
	fail := func(err error) workflowrunner.Result {
		result.Error = request.Redact(err.Error())
		if ctx.Err() != nil {
			result.State = "cancelled"
		}
		return result
	}
	if err := request.Validate(); err != nil {
		return fail(err)
	}
	op := request.Deployment
	if op == nil {
		return fail(errors.New("deployment request is missing"))
	}
	if !filepath.IsAbs(workspace) {
		return fail(errors.New("execution workspace must be absolute"))
	}
	info, err := os.Lstat(workspace)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fail(errors.New("execution workspace must be a private directory"))
	}
	app, server, d := op.App, op.Server, op.Deployment
	app.SourceCredential = op.SourceCredential
	app.ComposeContent = op.ComposeContent
	app.HookEnvironment = op.HookEnvironment
	app.ServiceRuntime = op.ServiceRuntime
	app.HelmValues = op.HelmValues
	app.HelmGeneratedValues = op.HelmGeneratedValues
	app.HelmGroupValues = op.HelmGroupValues
	app.HelmProvenance = op.HelmProvenance
	d.Snapshot = op.Snapshot
	if err = validateDeploymentInputs(app, server, op); err != nil {
		return fail(err)
	}
	if server.Kubernetes != nil {
		config := *server.Kubernetes
		config.KubeconfigPath = ""
		config.KubeconfigData = op.Kubeconfig
		config.CertificateAuthorityData = op.CertificateAuthority
		server.Kubernetes = &config
	}
	// Docker refers to the explicitly mounted tenant worker daemon. Payloads
	// cannot select DOCKER_HOST or an arbitrary remote daemon.
	if server.Runtime == core.ServerRuntimeDocker {
		server.Address = "local"
		server.AgentNodeID = ""
	}
	evidence := deploymentEvidence{result: &result, request: request}
	var executor deploy.Executor = deploy.HookExecutor{Next: deploy.SnapshotExecutor{Next: runtime(&result), Store: evidence}, Outputs: evidence}
	ctx = deploy.WithHealthReporter(ctx, func(_ context.Context, _ string, health core.DeploymentHealth) error {
		result.Health = &health
		return nil
	})
	ctx = deploy.WithRouteReporter(ctx, func(context.Context, core.ApplicationRoute) error {
		return errors.New("worker managed routing is unavailable")
	})
	report := func(state core.DeploymentState, message string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		result.Log = request.Redact(result.Log + fmt.Sprintf("[%s] %s\n", state, message))
		if progress != nil {
			progress(result.Log)
		}
		return nil
	}
	if op.Operation == "destroy" {
		err = executor.(deploy.CleanupExecutor).Cleanup(ctx, app, server, report)
	} else {
		err = executor.Deploy(ctx, d, app, server, report)
	}
	if err != nil {
		return fail(err)
	}
	result.State = "succeeded"
	return result
}

func validateDeploymentInputs(app core.App, server core.Server, op *workflowrunner.Deployment) error {
	if server.Routing != nil && app.BuildType != core.BuildTypeHelm {
		return errors.New("managed Docker routing requires a persistent worker route provider")
	}
	if app.SourceRepo != "" {
		if !workflowrunner.ValidRepositoryURL(app.SourceRepo) || !strings.HasPrefix(app.SourceRepo, "https://") && app.SourceAuthType != deploy.SourceAuthSSHKey {
			return errors.New("worker repositories require HTTPS or explicitly authenticated SSH")
		}
	}
	for name := range app.HookEnvironment {
		if _, _, secret := core.ParseSecretEnvironmentKey(name); secret {
			return errors.New("worker hook secrets must already be resolved")
		}
	}
	if app.BuildType == core.BuildTypeHelm {
		if server.Kubernetes == nil || op.Kubeconfig == "" || server.Kubernetes.KubeconfigPath != "" {
			return errors.New("worker Kubernetes targets require embedded credentials without controller paths")
		}
		if _, err := kubeconfig.ValidateStored([]byte(op.Kubeconfig), []byte(op.CertificateAuthority), server.Kubernetes.Context); err != nil {
			return err
		}
		config, err := clientcmd.Load([]byte(op.Kubeconfig))
		if err != nil {
			return errors.New("invalid worker kubeconfig")
		}
		for _, user := range config.AuthInfos {
			if user.Exec != nil || user.AuthProvider != nil || user.TokenFile != "" || user.ClientKey != "" || user.ClientCertificate != "" {
				return errors.New("worker kubeconfig cannot use plugins or external credential files")
			}
		}
		for _, cluster := range config.Clusters {
			if cluster.CertificateAuthority != "" && op.CertificateAuthority == "" {
				return errors.New("worker kubeconfig requires embedded certificate authorities")
			}
		}
	} else if server.Runtime != core.ServerRuntimeDocker || server.AgentNodeID == "" || (app.BuildType != core.BuildTypeDockerfile && app.BuildType != core.BuildTypeCompose) {
		return errors.New("worker Docker targets require an explicit node binding")
	}
	return nil
}
