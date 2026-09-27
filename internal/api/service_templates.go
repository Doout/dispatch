package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/serviceconn"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflow"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type serviceTemplateView struct {
	ID          string                                        `json:"id"`
	Name        string                                        `json:"name"`
	ProjectID   string                                        `json:"projectId"`
	Description string                                        `json:"description"`
	ServiceType string                                        `json:"serviceType"`
	Inputs      map[string]workflow.ServiceTemplateInputSpec  `json:"inputs"`
	Outputs     map[string]workflow.ServiceTemplateOutputSpec `json:"outputs"`
	ConfigSHA   string                                        `json:"configSha"`
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
		result = append(result, serviceTemplateView{ID: resource.ID, Name: resource.Name, ProjectID: source.ProjectID, Description: spec.Description, ServiceType: spec.ServiceType, Inputs: spec.Inputs, Outputs: spec.Outputs, ConfigSHA: resource.ConfigSHA})
	}
	writeJSON(w, 200, result)
}

type serviceProvisionRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Inputs      map[string]string `json:"inputs"`
}

func (a *API) startServiceProvision(w http.ResponseWriter, r *http.Request) {
	resource, err := a.store.GetWorkflowResource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Service template")
		return
	}
	if !resource.Active || resource.Kind != workflow.KindServiceTemplate {
		problem(w, 404, "Service template unavailable", "Choose an active service template.")
		return
	}
	config, err := a.store.GetConfigSource(r.Context(), resource.ConfigSourceID)
	if err != nil {
		a.internal(w, err)
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectConfigure, config.ProjectID) {
		return
	}
	if !a.requireProject(w, r, core.PermissionDeploymentRun, config.ProjectID) {
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
	services, err := a.store.ListServices(r.Context(), config.ProjectID)
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
	activeRuns, err := a.store.ListServiceProvisionRuns(r.Context(), config.ProjectID)
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
			if err != nil || item.ProjectID != config.ProjectID || item.Type != field.ServiceType {
				problem(w, 400, "Invalid service input", "Choose an available service in this project.")
				return
			}
			resolved, err := (serviceconn.Resolver{Vault: a.eventConfig.Vault, Secrets: a.secretResolver}).Resolve(r.Context(), item)
			if err != nil {
				problem(w, 400, "Service unavailable", "The selected service credentials could not be resolved.")
				return
			}
			value = resolved["connectionUrl"]
		}
		values[name] = value
	}
	run := core.ServiceProvisionRun{ID: ulid.Make().String(), TemplateID: resource.ID, ProjectID: config.ProjectID, ServiceName: input.Name, State: "queued", CreatedAt: time.Now().UTC()}
	if err := a.store.CreateServiceProvisionRun(r.Context(), run); err != nil {
		activeRuns, lookupErr := a.store.ListServiceProvisionRuns(r.Context(), config.ProjectID)
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
	go a.executeServiceProvision(resource, *spec, input.Description, values, run)
	writeJSON(w, http.StatusAccepted, run)
}

func (a *API) executeServiceProvision(resource core.WorkflowResource, spec workflow.ServiceTemplateSpec, description string, inputs map[string]string, run core.ServiceProvisionRun) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	run.State, run.Phase, run.StartedAt = "running", "Running provisioner", &now
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
	outputs, err := a.workflows.ProvisionService(ctx, resource, inputs)
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
		TemplateID: resource.ID, TemplateName: resource.Name, TemplateConfigSHA: resource.ConfigSHA, ProvisionRunID: run.ID}
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
