package workflowrunnerexec

import (
	"context"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/workflowrunner"
)

func executeServiceProvision(ctx context.Context, request workflowrunner.Request) workflowrunner.Result {
	executor := deploy.HelmExecutor{}
	return executeServiceOperationUsing(ctx, request, helmServiceOperations{provision: executor.Provision, inspect: executor.InspectServiceResource, remove: executor.DeleteServiceResource})
}

func executeServiceProvisionUsing(ctx context.Context, request workflowrunner.Request, provision func(context.Context, core.ServiceProvisionRequest, core.HelmServiceProvision, core.Server) (map[string]string, error)) workflowrunner.Result {
	return executeServiceOperationUsing(ctx, request, helmServiceOperations{provision: provision})
}

type helmServiceOperations struct {
	provision func(context.Context, core.ServiceProvisionRequest, core.HelmServiceProvision, core.Server) (map[string]string, error)
	inspect   func(context.Context, core.ServiceProvisionRequest, core.HelmServiceProvision, core.Server) (core.ServiceResourceInspection, error)
	remove    func(context.Context, core.ServiceProvisionRequest, core.HelmServiceProvision, core.Server, string) error
}

func executeServiceOperationUsing(ctx context.Context, request workflowrunner.Request, executor helmServiceOperations) workflowrunner.Result {
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
	p := request.ServiceProvision
	op := &workflowrunner.Deployment{Kubeconfig: p.Kubeconfig, CertificateAuthority: p.CertificateAuthority}
	if err := validateDeploymentInputs(core.App{BuildType: core.BuildTypeHelm}, p.Server, op); err != nil {
		return fail(err)
	}
	server := p.Server
	config := *server.Kubernetes
	config.KubeconfigPath = ""
	config.KubeconfigData = p.Kubeconfig
	config.CertificateAuthorityData = p.CertificateAuthority
	server.Kubernetes = &config
	switch p.Action() {
	case "inspect":
		if executor.inspect == nil {
			return fail(errors.New("service inspection is unavailable"))
		}
		inspection, err := executor.inspect(ctx, p.Request, p.Spec, server)
		if err != nil {
			return fail(err)
		}
		return workflowrunner.Result{State: "succeeded", ServiceInspection: &inspection}
	case "delete":
		if executor.remove == nil {
			return fail(errors.New("service cleanup is unavailable"))
		}
		if err := executor.remove(ctx, p.Request, p.Spec, server, p.ExpectedResource); err != nil {
			return fail(err)
		}
		return workflowrunner.Result{State: "succeeded"}
	default:
		if executor.provision == nil {
			return fail(errors.New("service provisioning is unavailable"))
		}
		outputs, err := executor.provision(ctx, p.Request, p.Spec, server)
		if err != nil {
			return fail(err)
		}
		// Outputs travel encrypted to the owning service provisioning record.
		return workflowrunner.Result{State: "succeeded", Outputs: outputs}
	}
}
