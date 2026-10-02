package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/neon"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflow"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type acceptedServiceResource struct {
	Neon         *acceptedNeonResource
	Request      core.ServiceProvisionRequest
	Docker       *core.DockerServiceProvision
	Helm         *core.HelmServiceProvision
	Outputs      map[string]workflow.ServiceTemplateOutputSpec
	TemplateName string
	Description  string
	NodeID       string
	Runtime      string
}

type serviceResourceRuntime interface {
	Provision(context.Context, acceptedServiceResource, core.Server) (map[string]string, error)
	Inspect(context.Context, acceptedServiceResource, core.Server) (core.ServiceResourceInspection, error)
	Delete(context.Context, acceptedServiceResource, core.Server, string, string) error
}
type defaultServiceResourceRuntime struct {
	remote deploy.RemoteExecutor
	api    *API
}

func (b defaultServiceResourceRuntime) Provision(ctx context.Context, r acceptedServiceResource, s core.Server) (map[string]string, error) {
	if r.Neon != nil {
		return nil, errors.New("Neon provisioning requires the durable phase checkpoint")
	}
	if r.Docker != nil {
		return b.remote.Provision(ctx, r.Request, *r.Docker, s)
	}
	return (deploy.HelmExecutor{}).Provision(ctx, r.Request, *r.Helm, s)
}
func (b defaultServiceResourceRuntime) Inspect(ctx context.Context, r acceptedServiceResource, s core.Server) (core.ServiceResourceInspection, error) {
	if r.Neon != nil {
		return b.api.inspectNeonResource(ctx, r)
	}
	if r.Docker != nil {
		return b.remote.InspectServiceResource(ctx, r.Request, *r.Docker, s)
	}
	return (deploy.HelmExecutor{}).InspectServiceResource(ctx, r.Request, *r.Helm, s)
}
func (b defaultServiceResourceRuntime) Delete(ctx context.Context, r acceptedServiceResource, s core.Server, expected, operation string) error {
	if r.Neon != nil {
		client, err := b.api.neonClient(r)
		if err != nil {
			return err
		}
		return client.Delete(ctx, r.Neon.Spec, r.Neon.Scope, expected)
	}
	if r.Docker != nil {
		return b.remote.DeleteServiceResource(ctx, r.Request, *r.Docker, s, expected, operation)
	}
	return (deploy.HelmExecutor{}).DeleteServiceResource(ctx, r.Request, *r.Helm, s, expected)
}
func (a *API) serviceResourceExecutor() serviceResourceRuntime {
	if a.serviceResourceBackend != nil {
		return a.serviceResourceBackend
	}
	return defaultServiceResourceRuntime{remote: deploy.RemoteExecutor{Local: deploy.DockerExecutor{}, Broker: a.runtimeBroker()}, api: a}
}
func (a *API) captureServiceResource(ctx context.Context, resource core.WorkflowResource, spec workflow.ServiceTemplateSpec, description string, inputs map[string]string, run core.ServiceProvisionRun, dependencies []string) error {
	data, ok := a.store.(store.ServiceResourceStore)
	if !ok || a.eventConfig.Vault == nil {
		return errors.New("encrypted service recovery storage is unavailable")
	}
	var server core.Server
	var err error
	if spec.Provision.Neon == nil {
		server, err = a.store.GetServer(ctx, run.Target.ServerID)
		if err != nil {
			return err
		}
	}
	req, err := deploy.PrepareServiceRequest(core.ServiceProvisionRequest{Run: run, ServiceType: spec.ServiceType, Inputs: inputs, Outputs: spec.Provision.Outputs, ConfigSHA: resource.ConfigSHA})
	if err != nil {
		return err
	}
	accepted := acceptedServiceResource{Request: req, Docker: spec.Provision.Docker, Helm: spec.Provision.Helm, Outputs: spec.Outputs, TemplateName: resource.Name, Description: description, NodeID: server.AgentNodeID, Runtime: server.Runtime}
	if spec.Provision.Neon != nil {
		n, err := a.resolveNeonSpec(ctx, run.ProjectID, *spec.Provision.Neon)
		if err != nil {
			return err
		}
		token, err := a.secretResolver.Resolve(ctx, n.CredentialRef)
		if err != nil {
			return errors.New("Neon scoped credential unavailable")
		}
		defer clear(token)
		approved, _ := ctx.Value(neonDataCopyApprovalKey{}).(bool)
		preview, _ := ctx.Value(neonPreviewAcceptanceKey{}).(neonPreviewAcceptance)
		generation := preview.Generation
		if generation == 0 {
			generation = 1
		}
		accepted.Neon = &acceptedNeonResource{Spec: n, Scope: neon.Scope{ProjectID: run.ProjectID, RunID: run.ID, PreviewID: preview.ID, Generation: generation, ConfigDigest: resource.ConfigSHA, ApprovedDataCopy: approved}, Token: string(token)}
	}
	if accepted.Helm != nil {
		copy := *accepted.Helm
		copy.Namespace = run.Target.Namespace
		accepted.Helm = &copy
	}
	raw, err := json.Marshal(accepted)
	if err != nil {
		return err
	}
	defer clear(raw)
	cipher, err := a.eventConfig.Vault.Encrypt("service-resource:"+run.ID+":request", raw)
	if err != nil {
		return err
	}
	record := core.ServiceResource{RunID: run.ID, ProjectID: run.ProjectID, ServiceID: ulid.Make().String(), Name: run.ServiceName, Target: *run.Target, State: "accepted", Policy: "retain", Revision: 1, OperationID: run.ID, CreatedAt: run.CreatedAt, UpdatedAt: run.CreatedAt, EncryptedRequest: cipher, Dependencies: dependencies}
	if preview, ok := ctx.Value(neonPreviewAcceptanceKey{}).(neonPreviewAcceptance); ok {
		record.PreviewID, record.PreviewAlias = preview.ID, preview.Alias
		record.ReplacesRunID, record.ReplacesRevision = preview.ReplacesRunID, preview.ReplacesRevision
	}
	return data.CreateServiceResource(ctx, run, record)
}
func (a *API) loadAcceptedServiceResource(ctx context.Context, record core.ServiceResource) (acceptedServiceResource, core.Server, error) {
	var accepted acceptedServiceResource
	if a.eventConfig.Vault == nil {
		return accepted, core.Server{}, errors.New("encrypted service recovery material is unavailable")
	}
	raw, err := a.eventConfig.Vault.Decrypt("service-resource:"+record.RunID+":request", record.EncryptedRequest)
	if err != nil {
		return accepted, core.Server{}, errors.New("encrypted service recovery material cannot be read")
	}
	defer clear(raw)
	if json.Unmarshal(raw, &accepted) != nil {
		return accepted, core.Server{}, errors.New("invalid service recovery material")
	}
	providers := 0
	if accepted.Docker != nil {
		providers++
	}
	if accepted.Helm != nil {
		providers++
	}
	if accepted.Neon != nil {
		providers++
	}
	if accepted.Request.Run.ID != record.RunID || accepted.Request.Run.ProjectID != record.ProjectID || accepted.Request.Run.Target == nil || *accepted.Request.Run.Target != record.Target || accepted.Request.Password == "" || providers != 1 {
		return accepted, core.Server{}, errors.New("service recovery ownership changed")
	}
	if accepted.Neon != nil {
		n := accepted.Neon
		if record.Target.Provider != "neon" || record.Target.ServerID != "" || n.Scope.RunID != record.RunID || n.Scope.ProjectID != record.ProjectID || n.Scope.ConfigDigest != accepted.Request.ConfigSHA {
			return accepted, core.Server{}, errors.New("Neon accepted ownership changed")
		}
		accepted.Neon.Scope.ExpectedBranchID = record.ResourceID
		return accepted, core.Server{}, nil
	}
	server, err := a.store.GetServer(ctx, record.Target.ServerID)
	if err != nil || server.AgentNodeID != accepted.NodeID || server.Runtime != accepted.Runtime {
		return accepted, server, errors.New("service target identity changed; inspect the original target")
	}
	return accepted, server, nil
}
func (a *API) executeOwnedServiceResource(runID string, retry bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	data := a.store.(store.ServiceResourceStore)
	record, err := data.GetServiceResource(ctx, runID)
	if err != nil {
		return
	}
	record, err = data.ClaimServiceResource(ctx, runID, record.Revision, runID, "recovering", ulid.Make().String(), time.Now().UTC(), "")
	if err != nil {
		return
	}
	run, err := a.store.GetServiceProvisionRun(ctx, runID)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	run.State, run.Phase, run.StartedAt = "running", "Inspecting owned service resource", &now
	_ = a.store.UpdateServiceProvisionRun(ctx, run)
	fail := func(message string) {
		finished := time.Now().UTC()
		record.State, record.Message = "unresolved", message
		run.State, run.Phase, run.Error, run.FinishedAt = "failed", "Recovery required", message, &finished
		if data.SaveServiceResource(context.WithoutCancel(ctx), record, run, nil) != nil {
			a.logger.Error("Service recovery evidence could not be saved", "run", runID)
		}
	}
	accepted, server, err := a.loadAcceptedServiceResource(ctx, record)
	if err != nil {
		fail("Service recovery material or target identity is unavailable. Inspect the original run.")
		return
	}
	var outputs map[string]string
	if accepted.Neon != nil {
		outputs, err = a.runNeonResource(ctx, &record, accepted, retry)
	} else {
		err = a.deploy.Storage.WithTarget(ctx, server.ID, func() error {
			inspected, e := a.serviceResourceExecutor().Inspect(ctx, accepted, server)
			if e != nil {
				return e
			}
			if inspected.RunID != runID || inspected.ProjectID != record.ProjectID || inspected.ServerID != server.ID || inspected.Provider != record.Target.Provider {
				return errors.New("ownership mismatch")
			}
			switch inspected.State {
			case "ready":
				outputs, e = deploy.ServiceResourceOutputs(accepted.Request, accepted.Docker, accepted.Helm)
			case "absent":
				if !retry {
					return errors.New("owned resource is absent; explicitly retry the accepted provision run")
				}
				accepted.Request.OperationID = fmt.Sprintf("service-%s-retry-%d", runID, record.Revision)
				outputs, e = a.serviceResourceExecutor().Provision(ctx, accepted, server)
				if e == nil {
					inspected, e = a.serviceResourceExecutor().Inspect(ctx, accepted, server)
					if e == nil && inspected.State != "ready" {
						e = errors.New("service did not become ready")
					}
				}
			default:
				return errors.New("owned resource is not ready; inspect it before recovery")
			}
			if e != nil {
				return e
			}
			record.ResourceID = inspected.ResourceID
			return a.deploy.Storage.RefreshLocked(ctx, server)
		})
	}
	if err != nil {
		fail("The owned service is unavailable or its outcome is uncertain. Inspect this run and target before retrying; retained data and credentials remain protected.")
		return
	}
	raw, err := json.Marshal(outputs)
	if err != nil {
		fail("Connection recovery material is invalid.")
		return
	}
	record.EncryptedOutputs, err = a.eventConfig.Vault.Encrypt("service-resource:"+runID+":outputs", raw)
	clear(raw)
	if err != nil {
		fail("Connection recovery material could not be encrypted.")
		return
	}
	item, err := a.serviceResourceConnection(ctx, record, accepted, outputs)
	if err != nil {
		fail("The returned connection is invalid. Inspect the original owned resource before retrying.")
		return
	}
	finished := time.Now().UTC()
	run.State, run.Phase, run.ServiceID, run.Error, run.FinishedAt = "succeeded", "Ready", item.ID, "", &finished
	record.State, record.Message = "ready", "Owned resource is ready and its encrypted connection is registered."
	if err = data.SaveServiceResource(ctx, record, run, &item); err != nil {
		fail("The resource exists but its connection could not be saved. Reconcile this run to recover the same resource and credentials.")
	}
}
func (a *API) serviceResourceConnection(ctx context.Context, record core.ServiceResource, accepted acceptedServiceResource, outputs map[string]string) (core.Service, error) {
	input := serviceRequest{ProjectID: record.ProjectID, Name: record.Name, Description: accepted.Description, Type: accepted.Request.ServiceType, Fields: map[string]serviceFieldInput{}}
	for name, spec := range accepted.Outputs {
		value, ok := outputs[name]
		if !ok {
			return core.Service{}, errors.New("missing declared service output")
		}
		if name == "connectionUrl" {
			input.ConnectionURL = &value
			continue
		}
		sensitive := spec.Sensitive || name == "password"
		input.Fields[name] = serviceFieldInput{Value: &value, Sensitive: &sensitive}
	}
	now := time.Now().UTC()
	item := core.Service{ID: record.ServiceID, Revision: 1, CreatedAt: now, UpdatedAt: now, TemplateID: accepted.Request.Run.TemplateID, TemplateName: accepted.TemplateName, TemplateConfigSHA: accepted.Request.ConfigSHA, ProvisionRunID: record.RunID, ProvisionTarget: &record.Target}
	return a.serviceInput((&http.Request{}).WithContext(ctx), input, item)
}
func (a *API) serviceResourceRecord(w http.ResponseWriter, r *http.Request, permission core.Permission) (core.ServiceResource, bool) {
	data, ok := a.store.(store.ServiceResourceStore)
	if !ok {
		problem(w, 503, "Service recovery unavailable", "Durable ownership storage is required.")
		return core.ServiceResource{}, false
	}
	record, err := data.GetServiceResource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Owned service resource")
		return record, false
	}
	return record, a.requireProject(w, r, permission, record.ProjectID)
}
func (a *API) getServiceResource(w http.ResponseWriter, r *http.Request) {
	record, ok := a.serviceResourceRecord(w, r, core.PermissionProjectView)
	if ok {
		writeJSON(w, 200, record)
	}
}
func (a *API) inspectServiceResource(w http.ResponseWriter, r *http.Request) {
	record, ok := a.serviceResourceRecord(w, r, core.PermissionProjectConfigure)
	if !ok {
		return
	}
	accepted, server, err := a.loadAcceptedServiceResource(r.Context(), record)
	if err != nil {
		problem(w, 409, "Recovery unavailable", "The accepted resource identity cannot be inspected.")
		return
	}
	result, err := a.inspectServiceResourceRecord(r.Context(), record, accepted, server)
	if err != nil {
		problem(w, 409, "Inspection unavailable", "Inspect the original target and its owned service resource.")
		return
	}
	writeJSON(w, 200, result)
}
func (a *API) recoverServiceResource(w http.ResponseWriter, r *http.Request) {
	record, ok := a.serviceResourceRecord(w, r, core.PermissionProjectConfigure)
	if !ok {
		return
	}
	if !a.requireProject(w, r, core.PermissionDeploymentRun, record.ProjectID) {
		return
	}
	if record.State == "deleted" || record.LeaseUntil.After(time.Now()) {
		problem(w, 409, "Recovery unavailable", "The resource is deleted or its original operation is still active.")
		return
	}
	if record.OperationID != record.RunID {
		if record.Target.Provider == "neon" {
			accepted, _, err := a.loadAcceptedServiceResource(r.Context(), record)
			if err != nil {
				problem(w, 409, "Cleanup inspection unavailable", "Inspect the captured Neon branch before recovery.")
				return
			}
			observed, err := a.inspectServiceResourceRecord(r.Context(), record, accepted, core.Server{})
			if err != nil || observed.State != "absent" {
				problem(w, 409, "Cleanup outcome unresolved", "The captured branch is still present or cannot be verified. Reconciliation never repeats an uncertain delete; review a separate explicit deletion if needed.")
				return
			}
		}
		claimed, err := a.store.(store.ServiceResourceStore).ClaimServiceResource(r.Context(), record.RunID, record.Revision, record.OperationID, "deleting", ulid.Make().String(), time.Now().UTC(), record.ResourceID)
		if err != nil {
			problem(w, 409, "Cleanup recovery unavailable", "Consumers or the original operation changed. Inspect and review the owned resource.")
			return
		}
		go a.executeServiceResourceDelete(claimed)
		core.RecordAcceptedOperation(r.Context(), record.OperationID)
		writeJSON(w, 202, claimed)
		return
	}
	retry := chi.URLParam(r, "action") == "retry"
	go a.executeOwnedServiceResource(record.RunID, retry)
	record.State = "recovering"
	core.RecordAcceptedOperation(r.Context(), record.RunID)
	writeJSON(w, 202, record)
}
func (a *API) deleteServiceResourceRequest(w http.ResponseWriter, r *http.Request) {
	record, ok := a.serviceResourceRecord(w, r, core.PermissionProjectConfigure)
	if !ok {
		return
	}
	var input struct {
		Confirmation destructiveConfirmation `json:"confirmation"`
	}
	if !decode(w, r, &input) {
		return
	}
	r, receipt, proceed := a.reserveMutation(w, r, record.ProjectID, "service.resource.delete", input, "service_resource", record.RunID)
	if !proceed {
		return
	}
	raw, _ := json.Marshal(input)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	completed := false
	handler := func(w http.ResponseWriter, r *http.Request) {
		err := a.withStorageRegistrationRemoval(r, "service-resource", record.RunID, func() error {
			current, e := a.store.(store.ServiceResourceStore).GetServiceResource(r.Context(), record.RunID)
			if e != nil {
				return e
			}
			operation := ulid.Make().String()
			if receipt != nil {
				operation = receipt.OperationID
			}
			accepted, server, e := a.loadAcceptedServiceResource(r.Context(), current)
			if e != nil {
				return e
			}
			inspected, e := a.inspectServiceResourceRecord(r.Context(), current, accepted, server)
			if e != nil {
				return e
			}
			if inspected.ResourceID != "" {
				current.ResourceID = inspected.ResourceID
			}
			claimed, e := a.store.(store.ServiceResourceStore).ClaimServiceResource(r.Context(), current.RunID, current.Revision, operation, "deleting", ulid.Make().String(), time.Now().UTC(), current.ResourceID)
			if e != nil {
				return e
			}
			core.RecordAcceptedOperation(r.Context(), operation)
			go a.executeServiceResourceDelete(claimed)
			completed = true
			if receipt != nil {
				saved, e := a.store.(store.MutationReceiptStore).GetMutationReceipt(r.Context(), receipt.ID)
				if e != nil {
					return e
				}
				a.writeMutationReceipt(w, r, saved, 202)
			} else {
				writeJSON(w, 202, claimed)
			}
			return nil
		})
		if err != nil {
			problem(w, 409, "Deletion requires review", "The owned resource, consumers or storage changed. Inspect and review deletion again.")
		}
	}
	a.confirmDestructiveAction("service-resource", "delete", handler).ServeHTTP(w, r)
	if !completed {
		a.failMutationAcceptance(r.Context(), 409, "Service deletion was rejected; inspect its consumers and protected storage before reviewing again.")
	}
}
func (a *API) executeServiceResourceDelete(record core.ServiceResource) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	run, err := a.store.GetServiceProvisionRun(ctx, record.RunID)
	if err != nil {
		return
	}
	accepted, server, err := a.loadAcceptedServiceResource(ctx, record)
	if err == nil && neverStartedNeonReplacement(record) {
		// The deletion claim fenced every late create checkpoint before cancellation.
	} else if err == nil && accepted.Neon != nil {
		err = a.serviceResourceExecutor().Delete(ctx, accepted, server, record.ResourceID, record.OperationID)
	} else if err == nil {
		err = a.deploy.Storage.WithTarget(ctx, server.ID, func() error {
			if e := a.deploy.Storage.RefreshLocked(ctx, server); e != nil {
				return e
			}
			inspected, e := a.serviceResourceExecutor().Inspect(ctx, accepted, server)
			if e != nil {
				return e
			}
			if inspected.State != "absent" {
				e = a.serviceResourceExecutor().Delete(ctx, accepted, server, record.ResourceID, fmt.Sprintf("%s-retry-%d", record.OperationID, record.Revision))
			}
			if e == nil {
				e = a.deploy.Storage.RefreshLocked(ctx, server)
			}
			return e
		})
	}
	record.State, record.Message = "deleted", "Owned workload removed. Storage and encrypted recovery history retained."
	if accepted.Neon != nil {
		record.Message = "Owned Neon branch and its data deleted. Encrypted operation history retained."
	}
	if err != nil {
		record.State, record.Message = "unresolved", "Cleanup is incomplete or uncertain. Inspect the same owned resource before another reviewed deletion."
		if accepted.Neon != nil {
			record.State = "deleting"
		}
	}
	if record.ReplacesRunID != "" && record.State == "deleted" && run.State != "succeeded" {
		finished := time.Now().UTC()
		run.State, run.Phase, run.Error, run.FinishedAt = "failed", "Replacement cancelled", "Candidate removed by explicit review; original branch retained.", &finished
	}
	if e := a.store.(store.ServiceResourceStore).SaveServiceResource(context.WithoutCancel(ctx), record, run, nil); e != nil {
		a.logger.Error("Service cleanup evidence could not be saved", "run", record.RunID)
	}
}

func (a *API) serviceResourcePermission(permission core.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := a.serviceResourceRecord(w, r, permission); ok {
				next.ServeHTTP(w, r)
			}
		})
	}
}

func neverStartedNeonReplacement(r core.ServiceResource) bool {
	return r.Target.Provider == "neon" && r.ReplacesRunID != "" && !r.ProviderCreateAttempted && r.ResourceID == ""
}
func (a *API) inspectServiceResourceRecord(ctx context.Context, record core.ServiceResource, accepted acceptedServiceResource, server core.Server) (core.ServiceResourceInspection, error) {
	if neverStartedNeonReplacement(record) {
		return core.ServiceResourceInspection{RunID: record.RunID, ProjectID: record.ProjectID, Provider: "neon", State: "absent"}, nil
	}
	return a.serviceResourceExecutor().Inspect(ctx, accepted, server)
}
