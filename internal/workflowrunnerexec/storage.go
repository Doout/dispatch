package workflowrunnerexec

import (
	"context"
	"errors"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/workflowrunner"
)

func executeStorage(ctx context.Context, r workflowrunner.Request, backend deploy.StorageBackend) workflowrunner.Result {
	fail := func(err error) workflowrunner.Result {
		return workflowrunner.Result{State: "failed", Error: r.Redact(err.Error())}
	}
	if err := r.Validate(); err != nil {
		return fail(err)
	}
	p := r.StorageOperation
	if p == nil {
		return fail(errors.New("storage operation missing"))
	}
	if err := validateDeploymentInputs(core.App{BuildType: core.BuildTypeHelm}, p.Server, &workflowrunner.Deployment{Kubeconfig: p.Kubeconfig, CertificateAuthority: p.CertificateAuthority}); err != nil {
		return fail(err)
	}
	server := p.Server
	config := *server.Kubernetes
	config.KubeconfigPath = ""
	config.KubeconfigData = p.Kubeconfig
	config.CertificateAuthorityData = p.CertificateAuthority
	server.Kubernetes = &config
	if p.Resource != nil {
		if reason := p.Resource.DeleteBlockedReason(); reason != "" {
			return fail(errors.New(reason))
		}
		if err := backend.Delete(ctx, server, *p.Resource); err != nil {
			return fail(err)
		}
		return workflowrunner.Result{State: "succeeded"}
	}
	items, err := backend.Inspect(ctx, server)
	if err != nil {
		return fail(err)
	}
	if items == nil {
		items = []core.StorageObservation{}
	}
	return workflowrunner.Result{State: "succeeded", Storage: items}
}
