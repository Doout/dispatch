package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/serviceconn"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflow"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type serviceTemplateView struct {
	Provider       string                                        `json:"provider"`
	ID             string                                        `json:"id"`
	Name           string                                        `json:"name"`
	ProjectID      string                                        `json:"projectId"`
	Description    string                                        `json:"description"`
	ServiceType    string                                        `json:"serviceType"`
	Inputs         map[string]workflow.ServiceTemplateInputSpec  `json:"inputs"`
	Outputs        map[string]workflow.ServiceTemplateOutputSpec `json:"outputs"`
	ManagedBy      string                                        `json:"managedBy"`
	Revision       int64                                         `json:"revision,omitempty"`
	ConfigSourceID string                                        `json:"configSourceId,omitempty"`
	Document       string                                        `json:"document,omitempty"`
	ConfigSHA      string                                        `json:"configSha"`
}

func (a *API) listServiceTemplates(w http.ResponseWriter, r *http.Request) {
	resources, err := a.store.ListWorkflowResources(r.Context(), "")
	if err != nil {
		a.internal(w, err)
		return
	}
	visible, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	result := []serviceTemplateView{}
	for _, resource := range resources {
		if !resource.Active || resource.Kind != workflow.KindServiceTemplate {
			continue
		}
		source, err := a.store.GetConfigSource(r.Context(), resource.ConfigSourceID)
		if err != nil {
			a.internal(w, err)
			return
		}
		if !visible[source.ProjectID] {
			continue
		}
		documents, err := workflow.Parse(resource.Path, []byte(resource.Document))
		if err != nil || len(documents) != 1 || documents[0].ServiceTemplate == nil {
			continue
		}
		spec := documents[0].ServiceTemplate
		result = append(result, serviceTemplateView{ID: resource.ID, Name: resource.Name, ProjectID: source.ProjectID, Description: spec.Description, ServiceType: spec.ServiceType, Provider: spec.Provision.Provider(), Inputs: spec.Inputs, Outputs: spec.Outputs, ConfigSHA: resource.ConfigSHA, ManagedBy: "gitops", ConfigSourceID: resource.ConfigSourceID})
	}
	saved, err := a.store.ListSavedServiceTemplates(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, item := range saved {
		if !visible[item.ProjectID] {
			continue
		}
		documents, err := workflow.Parse("template.yaml", []byte(item.Document))
		if err != nil || len(documents) != 1 || documents[0].ServiceTemplate == nil {
			continue
		}
		spec := documents[0].ServiceTemplate
		result = append(result, serviceTemplateView{ID: item.ID, Name: item.Name, ProjectID: item.ProjectID, Description: spec.Description, ServiceType: spec.ServiceType, Provider: spec.Provision.Provider(), Inputs: spec.Inputs, Outputs: spec.Outputs, ConfigSHA: item.Digest, ManagedBy: "dispatch", Revision: item.Revision, ConfigSourceID: item.ConfigSourceID})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].ID < result[j].ID
		}
		return result[i].Name < result[j].Name
	})
	writeJSON(w, 200, result)
}

type serviceProvisionRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Inputs      map[string]string `json:"inputs"`
}

func (a *API) startServiceProvision(w http.ResponseWriter, r *http.Request) {
	resource, projectID, _, err := a.serviceTemplateResource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Service template")
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectConfigure, projectID) {
		return
	}
	if !a.requireProject(w, r, core.PermissionDeploymentRun, projectID) {
		return
	}
	if a.workflows == nil {
		problem(w, 503, "Provisioning unavailable", "Workflow runner is not configured.")
		return
	}
	var input serviceProvisionRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !serviceconn.NamePattern.MatchString(input.Name) {
		problem(w, 400, "Invalid service name", "Use lowercase letters, digits, dots, or hyphens.")
		return
	}
	documents, err := workflow.Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].ServiceTemplate == nil {
		problem(w, 409, "Invalid template", "Sync the template configuration again.")
		return
	}
	spec := documents[0].ServiceTemplate
	if len(spec.Sources) > 0 {
		source, err := a.store.GetConfigSource(r.Context(), resource.ConfigSourceID)
		if err != nil || source.ProjectID != projectID {
			problem(w, 409, "Repository access unavailable", "Select a repository connection in this template's project.")
			return
		}
	}
	target, err := a.serviceProvisionTarget(r.Context(), *spec)
	if err != nil {
		problem(w, 400, "Invalid provisioner target", err.Error())
		return
	}
	r, receipt, proceed := a.reserveMutation(w, r, projectID, "service.provision", struct {
		TemplateID string
		Input      serviceProvisionRequest
	}{resource.ID, input}, "service_provision", resource.ID)
	if !proceed {
		return
	}
	acceptedReceipt := false
	defer func() {
		if !acceptedReceipt {
			a.failMutationAcceptance(r.Context(), 422, "Service acceptance was rejected. Inspect the template, inputs and original run before retrying.")
		}
	}()
	services, err := a.store.ListServices(r.Context(), projectID)
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, item := range services {
		if item.Name == input.Name {
			problem(w, 409, "Service already exists", "Choose a unique service name.")
			return
		}
	}
	activeRuns, err := a.store.ListServiceProvisionRuns(r.Context(), projectID)
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, active := range activeRuns {
		if active.ServiceName == input.Name && (active.State == "queued" || active.State == "running") {
			problem(w, 409, "Service is provisioning", "Wait for the existing run to finish.")
			return
		}
	}
	dependencies := []string{}
	values := map[string]string{}
	for name := range input.Inputs {
		if _, ok := spec.Inputs[name]; !ok {
			problem(w, 400, "Unknown input", "The template does not accept "+name+".")
			return
		}
	}
	for name, field := range spec.Inputs {
		value := input.Inputs[name]
		if field.Required && strings.TrimSpace(value) == "" {
			problem(w, 400, "Missing input", "Provide "+name+".")
			return
		}
		if len(value) > 65536 {
			problem(w, 400, "Input too long", "Inputs must be at most 64 KiB.")
			return
		}
		if field.Type == "service" && value != "" {
			item, err := a.store.GetService(r.Context(), value)
			if err != nil || item.ProjectID != projectID || item.Type != field.ServiceType {
				problem(w, 400, "Invalid service input", "Choose an available service in this project.")
				return
			}
			dependencies = append(dependencies, item.ID)
			resolved, err := (serviceconn.Resolver{Vault: a.eventConfig.Vault, Secrets: a.secretResolver}).Resolve(r.Context(), item)
			if err != nil {
				problem(w, 400, "Service unavailable", "The selected service credentials could not be resolved.")
				return
			}
			value = resolved["connectionUrl"]
		}
		values[name] = value
	}
	run := core.ServiceProvisionRun{ID: ulid.Make().String(), TemplateID: resource.ID, ProjectID: projectID, ServiceName: input.Name, State: "queued", Target: target, CreatedAt: time.Now().UTC()}
	if receipt != nil {
		run.ID = receipt.OperationID
	}
	if run.Target != nil {
		run.Target.ResourceName = deploy.ServiceResourceName(run.ID)
	}
	if run.Target != nil {
		if err := a.captureServiceResource(r.Context(), resource, *spec, input.Description, values, run, dependencies); err != nil {
			problem(w, 409, "Service acceptance unavailable", "The service name or dependencies changed, or encrypted recovery storage is unavailable.")
			return
		}
		acceptedReceipt = true
		core.RecordAcceptedOperation(r.Context(), run.ID)
		go a.executeOwnedServiceResource(run.ID, true)
		if receipt != nil {
			saved, err := a.store.(store.MutationReceiptStore).GetMutationReceipt(r.Context(), receipt.ID)
			if err != nil {
				a.internal(w, err)
				return
			}
			a.writeMutationReceipt(w, r, saved, 202)
		} else {
			writeJSON(w, 202, run)
		}
		return
	}
	if err := a.store.CreateServiceProvisionRun(r.Context(), run); err != nil {
		activeRuns, lookupErr := a.store.ListServiceProvisionRuns(r.Context(), projectID)
		if lookupErr == nil {
			for _, active := range activeRuns {
				if active.ServiceName == input.Name && (active.State == "queued" || active.State == "running") {
					problem(w, 409, "Service is provisioning", "Wait for the existing run to finish.")
					return
				}
			}
		}
		a.internal(w, err)
		return
	}
	acceptedReceipt = true
	core.RecordAcceptedOperation(r.Context(), run.ID)
	go a.executeServiceProvision(resource, *spec, input.Description, values, run)
	if receipt != nil {
		saved, err := a.store.(store.MutationReceiptStore).GetMutationReceipt(r.Context(), receipt.ID)
		if err != nil {
			a.internal(w, err)
			return
		}
		a.writeMutationReceipt(w, r, saved, 202)
	} else {
		writeJSON(w, http.StatusAccepted, run)
	}
}

func (a *API) executeServiceProvision(resource core.WorkflowResource, spec workflow.ServiceTemplateSpec, description string, inputs map[string]string, run core.ServiceProvisionRun) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	run.State, run.Phase, run.StartedAt = "running", "Running provisioner", &now
	if run.Target != nil {
		run.Phase = "Installing service with " + run.Target.Provider + " and waiting for readiness"
	}
	if err := a.store.UpdateServiceProvisionRun(ctx, run); err != nil {
		a.logger.Error("Update service provision run", "run", run.ID, "error", err)
		return
	}
	fail := func(message string) {
		finished := time.Now().UTC()
		run.State, run.Error, run.FinishedAt = "failed", message, &finished
		if err := a.store.UpdateServiceProvisionRun(context.Background(), run); err != nil {
			a.logger.Error("Update service provision run", "run", run.ID, "error", err)
		}
	}
	var outputs map[string]string
	var err error
	if run.Target != nil {
		err = a.deploy.Storage.WithTarget(ctx, run.Target.ServerID, func() error {
			var provisionErr error
			outputs, provisionErr = a.workflows.ProvisionService(ctx, resource, run.ProjectID, inputs, run)
			server, lookupErr := a.store.GetServer(ctx, run.Target.ServerID)
			if lookupErr != nil {
				return errors.Join(provisionErr, lookupErr)
			}
			return errors.Join(provisionErr, a.deploy.Storage.RefreshLocked(ctx, server))
		})
	} else {
		outputs, err = a.workflows.ProvisionService(ctx, resource, run.ProjectID, inputs, run)
	}
	if err != nil {
		fail(err.Error())
		return
	}
	run.Phase = "Saving connection"
	if err := a.store.UpdateServiceProvisionRun(ctx, run); err != nil {
		fail("Could not update provisioning status; check the provider before retrying.")
		return
	}
	request := serviceRequest{ProjectID: run.ProjectID, Name: run.ServiceName, Description: description, Type: spec.ServiceType, Fields: map[string]serviceFieldInput{}}
	for name, output := range spec.Outputs {
		value, ok := outputs[name]
		if !ok {
			fail("Provisioner did not return a declared output; check the provider before retrying.")
			return
		}
		if name == "connectionUrl" {
			request.ConnectionURL = &value
			continue
		}
		sensitive := output.Sensitive || name == "password"
		request.Fields[name] = serviceFieldInput{Value: &value, Sensitive: &sensitive}
	}
	created := time.Now().UTC()
	item := core.Service{ID: ulid.Make().String(), Revision: 1, CreatedAt: created, UpdatedAt: created,
		TemplateID: resource.ID, TemplateName: resource.Name, TemplateConfigSHA: resource.ConfigSHA, ProvisionRunID: run.ID, ProvisionTarget: run.Target}
	// serviceInput does not read identity for value inputs. Its existing path
	// validates connection fields and encrypts sensitive values.
	item, err = a.serviceInput((&http.Request{}).WithContext(ctx), request, item)
	if err != nil {
		fail("Provisioner returned an invalid service connection; check the provider before retrying.")
		return
	}
	if err = a.store.CreateService(ctx, item); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			fail("A service with this name already exists; check the provider before retrying.")
		} else {
			fail("Could not save the provisioned service; check the provider before retrying.")
		}
		return
	}
	finished := time.Now().UTC()
	run.State, run.Phase, run.ServiceID, run.FinishedAt = "succeeded", "Ready", item.ID, &finished
	if err := a.store.UpdateServiceProvisionRun(context.Background(), run); err != nil {
		a.logger.Error("Update service provision run", "run", run.ID, "error", err)
	}
}

func (a *API) listServiceProvisionRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := a.store.ListServiceProvisionRuns(r.Context(), strings.TrimSpace(r.URL.Query().Get("projectId")))
	if err != nil {
		a.internal(w, err)
		return
	}
	visible, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	result := []core.ServiceProvisionRun{}
	for _, run := range runs {
		if visible[run.ProjectID] {
			result = append(result, run)
		}
	}
	writeJSON(w, 200, result)
}

func (a *API) getServiceProvisionRun(w http.ResponseWriter, r *http.Request) {
	run, err := a.store.GetServiceProvisionRun(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Service provision run")
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectView, run.ProjectID) {
		return
	}
	writeJSON(w, 200, run)
}

func (a *API) serviceProvisionTarget(ctx context.Context, spec workflow.ServiceTemplateSpec) (*core.ServiceProvisionTarget, error) {
	var target core.ServiceProvisionTarget
	if spec.Provision.Docker != nil {
		target.Provider, target.ServerID = "docker", spec.Provision.Docker.ServerRef
		target.Network = spec.Provision.Docker.Network
		if target.Network == "" {
			target.Network = deploy.DefaultServiceNetwork
		}
	}
	if spec.Provision.Helm != nil {
		target.Provider, target.ServerID = "helm", spec.Provision.Helm.ServerRef
	}
	if target.Provider == "" {
		return nil, nil
	}
	server, err := a.store.GetServer(ctx, target.ServerID)
	if err != nil {
		return nil, errors.New("choose an available deployment server")
	}
	if server.AgentNodeID != "" && (a.runtimeBroker() == nil || spec.ServiceType != "postgresql") {
		return nil, errors.New("this remote runtime requires encrypted storage and supports built-in PostgreSQL provisioning")
	}
	if err := deploy.ValidateServiceTarget(server, target.Provider); err != nil {
		return nil, err
	}
	if spec.Provision.Helm != nil {
		target.Namespace = deploy.ServiceProvisionNamespace(*spec.Provision.Helm, server)
	}
	return &target, nil
}
