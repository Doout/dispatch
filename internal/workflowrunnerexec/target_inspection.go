package workflowrunnerexec

import (
	"context"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowrunner"
)

func executeTargetInspection(ctx context.Context, request workflowrunner.Request, inspect func(context.Context, core.KubernetesServerConfig) (core.KubernetesTargetEvidence, error)) workflowrunner.Result {
	fail := func(err error) workflowrunner.Result {
		state := "failed"
		if ctx.Err() != nil {
			state = "cancelled"
		}
		return workflowrunner.Result{State: state, Error: request.Redact(err.Error())}
	}
	if err := request.Validate(); err != nil {
		return fail(err)
	}
	p := request.TargetInspection
	config := p.Config
	server := core.Server{Runtime: core.ServerRuntimeKubernetes, Kubernetes: &config}
	op := &workflowrunner.Deployment{Kubeconfig: p.Kubeconfig, CertificateAuthority: p.CertificateAuthority}
	if err := validateDeploymentInputs(core.App{BuildType: core.BuildTypeHelm}, server, op); err != nil {
		return fail(err)
	}
	config.KubeconfigPath = ""
	config.KubeconfigData = p.Kubeconfig
	config.CertificateAuthorityData = p.CertificateAuthority
	evidence, err := inspect(ctx, config)
	if err != nil {
		return fail(err)
	}
	return workflowrunner.Result{State: "succeeded", TargetEvidence: &evidence}
}
