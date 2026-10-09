package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/drift"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflow"
	"github.com/doout/dispatch/internal/workflowrunner"
)

// ConfigureHostedExecution installs remote execution before any poller starts.
// The hosted factory must never construct a local Docker or hook executor.
func (a *API) ConfigureHostedExecution(executor *deploy.HostedExecutor, broker *workflowrunner.Broker) error {
	if a.auth.Hosted == nil || executor == nil || broker == nil || a.workflows == nil {
		return errors.New("hosted execution requires tenant authentication and remote workers")
	}
	executor.Authorize = func(ctx context.Context, d core.Deployment, app core.App, server core.Server) error {
		if server.ProjectID != "" && server.ProjectID != app.ProjectID {
			return errors.New("deployment target belongs to another project")
		}
		if err := a.checkTemporaryExecution(ctx, app, d.CommitSHA); err != nil {
			return err
		}
		return a.workflows.CheckDeploymentTrust(ctx, app, d.CommitSHA)
	}
	executor.AuthorizeCleanup = func(ctx context.Context, operation string, app core.App, server core.Server) error {
		if server.ProjectID != "" && server.ProjectID != app.ProjectID {
			return errors.New("cleanup target belongs to another project")
		}
		data, ok := a.store.(interface {
			CheckWorkflowPreviewCleanup(context.Context, string, string) error
		})
		if !ok {
			return errors.New("preview cleanup authorization is unavailable")
		}
		// Closing previews must still be removable after their source trust or
		// lifetime expires. Their durable cleanup acceptance grants that action.
		return data.CheckWorkflowPreviewCleanup(ctx, app.ID, operation)
	}
	executor.AuthorizeProvision = func(ctx context.Context, request core.ServiceProvisionRequest, spec core.HelmServiceProvision, server core.Server) error {
		run, err := a.store.GetServiceProvisionRun(ctx, request.Run.ID)
		if err != nil {
			return err
		}
		if run.State != "running" || run.ProjectID != request.Run.ProjectID || run.TemplateID != request.Run.TemplateID || run.Target == nil || run.Target.ServerID != server.ID || server.ProjectID != "" && server.ProjectID != run.ProjectID {
			return errors.New("service provision acceptance changed")
		}
		resource, project, _, err := a.serviceTemplateResource(ctx, run.TemplateID)
		if err != nil {
			return err
		}
		if project != run.ProjectID || resource.ConfigSHA != request.ConfigSHA {
			return errors.New("service template ownership or revision changed")
		}
		docs, err := workflow.Parse(resource.Path, []byte(resource.Document))
		if err != nil || len(docs) != 1 || docs[0].ServiceTemplate == nil || docs[0].ServiceTemplate.Provision.Helm == nil {
			return errors.New("Helm service template is unavailable")
		}
		expected := *docs[0].ServiceTemplate.Provision.Helm
		expected.Namespace = run.Target.Namespace
		left, _ := json.Marshal(expected)
		right, _ := json.Marshal(spec)
		if string(left) != string(right) {
			return errors.New("service provision specification changed")
		}
		return nil
	}

	executor.AuthorizeService = func(ctx context.Context, op workflowrunner.ServiceProvision, server core.Server) error {
		data, ok := a.store.(store.ServiceResourceStore)
		if !ok {
			return errors.New("service resource storage unavailable")
		}
		record, err := data.GetServiceResource(ctx, op.Request.Run.ID)
		if errors.Is(err, store.ErrNotFound) && op.Action() == "provision" {
			return executor.AuthorizeProvision(ctx, op.Request, op.Spec, server)
		}
		if err != nil {
			return err
		}
		accepted, target, err := a.loadAcceptedServiceResource(ctx, record)
		if err != nil {
			return err
		}
		if accepted.Helm == nil || target.ID != server.ID || record.ProjectID != op.Request.Run.ProjectID || server.ProjectID != "" && server.ProjectID != record.ProjectID {
			return errors.New("service resource ownership changed")
		}
		expected := accepted.Request
		expected.OperationID = op.Request.OperationID
		left, _ := json.Marshal(expected)
		right, _ := json.Marshal(op.Request)
		expectedSpec, _ := json.Marshal(accepted.Helm)
		actualSpec, _ := json.Marshal(op.Spec)
		if string(left) != string(right) || string(expectedSpec) != string(actualSpec) {
			return errors.New("service request does not match its accepted inputs")
		}
		switch op.Action() {
		case "inspect":
			return nil
		case "provision":
			run, err := a.store.GetServiceProvisionRun(ctx, record.RunID)
			if err != nil {
				return err
			}
			if run.State != "running" || record.State != "recovering" || record.OperationID != record.RunID || op.Request.OperationID != fmt.Sprintf("service-%s-retry-%d", record.RunID, record.Revision) {
				return errors.New("service provisioning is no longer accepted")
			}
			return nil
		case "delete":
			retry := fmt.Sprintf("%s-retry-%d", record.OperationID, record.Revision)
			if record.State != "deleting" || op.ExpectedResource == "" || op.ExpectedResource != record.ResourceID || op.OperationID != record.OperationID && op.OperationID != retry {
				return errors.New("service deletion is no longer accepted")
			}
			return nil
		default:
			return errors.New("unknown service operation")
		}
	}
	// Drift readers use Kubernetes clients directly. Disable those readers until
	// an enrolled worker provides the matching read/repair operation.
	a.drift.Connect = func(context.Context, core.Server) (drift.Connection, error) {
		return drift.Connection{}, errors.New("live cluster inspection requires a tenant worker operation")
	}
	a.drift.ReadRelease = func(context.Context, core.Server, string, string, core.Deployment) (deploy.HelmDriftRelease, error) {
		return deploy.HelmDriftRelease{}, errors.New("live Helm inspection requires a tenant worker operation")
	}
	broker.Authorize = executor.AuthorizeRequest
	a.ConfigureWorkflowRunner(broker)
	a.workflows.RemoteHelmServices = executor.Provision
	remote := deploy.RemoteExecutor{Broker: a.runtimeBroker()}
	a.workflows.RemoteDockerServices = remote.Provision
	a.serviceResourceBackend = hostedServiceResourceRuntime{base: defaultServiceResourceRuntime{remote: remote, api: a}, executor: executor}
	return nil
}

type hostedServiceResourceRuntime struct {
	base     defaultServiceResourceRuntime
	executor *deploy.HostedExecutor
}

func (b hostedServiceResourceRuntime) Provision(ctx context.Context, r acceptedServiceResource, s core.Server) (map[string]string, error) {
	if r.Helm != nil {
		return b.executor.Provision(ctx, r.Request, *r.Helm, s)
	}
	if r.Docker != nil && s.AgentNodeID == "" {
		return nil, errors.New("hosted Docker services require an enrolled deployment agent")
	}
	return b.base.Provision(ctx, r, s)
}
func (b hostedServiceResourceRuntime) Inspect(ctx context.Context, r acceptedServiceResource, s core.Server) (core.ServiceResourceInspection, error) {
	if r.Helm != nil {
		return b.executor.InspectServiceResource(ctx, r.Request, *r.Helm, s)
	}
	if r.Docker != nil && s.AgentNodeID == "" {
		return core.ServiceResourceInspection{}, errors.New("hosted service inspection requires an enrolled deployment agent")
	}
	return b.base.Inspect(ctx, r, s)
}
func (b hostedServiceResourceRuntime) Delete(ctx context.Context, r acceptedServiceResource, s core.Server, expected, operation string) error {
	if r.Helm != nil {
		return b.executor.DeleteServiceResource(ctx, r.Request, *r.Helm, s, expected, operation)
	}
	if r.Docker != nil && s.AgentNodeID == "" {
		return errors.New("hosted service cleanup requires an enrolled deployment agent")
	}
	return b.base.Delete(ctx, r, s, expected, operation)
}
