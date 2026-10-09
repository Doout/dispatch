package deploy

import (
	"context"
	"errors"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowrunner"
	"github.com/oklog/ulid/v2"
)

// Provision returns service credentials only to the encrypted service record.
func (e *HostedExecutor) Provision(ctx context.Context, request core.ServiceProvisionRequest, spec core.HelmServiceProvision, server core.Server) (map[string]string, error) {
	request, err := PrepareServiceRequest(request)
	if err != nil {
		return nil, err
	}
	result, err := e.executeService(ctx, "provision", request, spec, server, "", request.Run.ID)
	return result.Outputs, err
}

func (e *HostedExecutor) InspectServiceResource(ctx context.Context, request core.ServiceProvisionRequest, spec core.HelmServiceProvision, server core.Server) (core.ServiceResourceInspection, error) {
	result, err := e.executeService(ctx, "inspect", request, spec, server, "", "inspect-"+ulid.Make().String())
	if err != nil {
		return core.ServiceResourceInspection{}, err
	}
	inspection := result.ServiceInspection
	if inspection == nil || inspection.RunID != request.Run.ID || inspection.ProjectID != request.Run.ProjectID || inspection.ServerID != server.ID || inspection.Provider != "helm" {
		return core.ServiceResourceInspection{}, errors.New("worker service inspection ownership changed")
	}
	return *inspection, nil
}

func (e *HostedExecutor) DeleteServiceResource(ctx context.Context, request core.ServiceProvisionRequest, spec core.HelmServiceProvision, server core.Server, expected, operation string) error {
	if operation == "" {
		return errors.New("service cleanup requires an accepted operation")
	}
	_, err := e.executeService(ctx, "delete", request, spec, server, expected, operation)
	return err
}

func (e *HostedExecutor) executeService(ctx context.Context, operation string, request core.ServiceProvisionRequest, spec core.HelmServiceProvision, server core.Server, expected, revision string) (workflowrunner.Result, error) {
	if e.Runner == nil || e.Store == nil || e.AuthorizeService == nil && (operation != "provision" || e.AuthorizeProvision == nil) || server.Kubernetes == nil || server.Kubernetes.KubeconfigData == "" {
		return workflowrunner.Result{}, errors.New("remote Helm service execution is unavailable")
	}
	op := &workflowrunner.ServiceProvision{Operation: operation, Request: request, Spec: spec, Server: server, ServerDigest: hostedServerDigest(server), ExpectedResource: expected, Kubeconfig: server.Kubernetes.KubeconfigData, CertificateAuthority: server.Kubernetes.CertificateAuthorityData}
	if operation == "delete" {
		op.OperationID = revision
	}
	r := workflowrunner.Request{Version: workflowrunner.Version, ProjectID: request.Run.ProjectID, ResourceID: request.Run.TemplateID, RevisionID: revision, Mode: "tenant", ServiceProvision: op}
	if err := e.AuthorizeRequest(ctx, r); err != nil {
		return workflowrunner.Result{}, err
	}
	result, err := e.Runner.Run(ctx, r, nil)
	if err != nil {
		if ctx.Err() != nil {
			return workflowrunner.Result{}, ctx.Err()
		}
		return workflowrunner.Result{}, errors.New(r.Redact(err.Error()))
	}
	if result.State != "succeeded" {
		if result.Error != "" {
			return workflowrunner.Result{}, errors.New(r.Redact(result.Error))
		}
		return workflowrunner.Result{}, errors.New("remote service operation did not succeed")
	}
	return result, nil
}
