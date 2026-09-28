package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflow"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

// Both saved and repository templates enter the same provisioning path.
func (a *API) serviceTemplateResource(ctx context.Context, id string) (core.WorkflowResource, string, int64, error) {
	item, err := a.store.GetSavedServiceTemplate(ctx, id)
	if err == nil {
		return core.WorkflowResource{ID: item.ID, ConfigSourceID: item.ConfigSourceID, APIVersion: workflow.APIVersion, Kind: workflow.KindServiceTemplate, Name: item.Name, Path: "template.yaml", Document: item.Document, SpecDigest: item.Digest, ConfigSHA: item.Digest, Active: true}, item.ProjectID, item.Revision, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return core.WorkflowResource{}, "", 0, err
	}
	resource, err := a.store.GetWorkflowResource(ctx, id)
	if err != nil {
		return resource, "", 0, err
	}
	if !resource.Active || resource.Kind != workflow.KindServiceTemplate {
		return resource, "", 0, store.ErrNotFound
	}
	source, err := a.store.GetConfigSource(ctx, resource.ConfigSourceID)
	return resource, source.ProjectID, 0, err
}

func (a *API) getServiceTemplate(w http.ResponseWriter, r *http.Request) {
	resource, project, revision, err := a.serviceTemplateResource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Service template")
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectView, project) {
		return
	}
	docs, err := workflow.Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(docs) != 1 || docs[0].ServiceTemplate == nil {
		problem(w, 409, "Invalid template", "The saved definition could not be read.")
		return
	}
	spec := docs[0].ServiceTemplate
	managedBy := "gitops"
	if revision > 0 {
		managedBy = "dispatch"
	}
	writeJSON(w, 200, serviceTemplateView{ID: resource.ID, Name: resource.Name, ProjectID: project, Description: spec.Description, ServiceType: spec.ServiceType, Inputs: spec.Inputs, Outputs: spec.Outputs, ConfigSHA: resource.ConfigSHA, ManagedBy: managedBy, Revision: revision, ConfigSourceID: resource.ConfigSourceID, Document: resource.Document})
}

type serviceTemplateRequest struct {
	ProjectID      string `json:"projectId"`
	ConfigSourceID string `json:"configSourceId"`
	Document       string `json:"document"`
	Revision       int64  `json:"revision"`
}

func (a *API) serviceTemplateInput(w http.ResponseWriter, r *http.Request, input serviceTemplateRequest, item *core.SavedServiceTemplate) bool {
	if strings.TrimSpace(input.Document) == "" || len(input.Document) > 256*1024 {
		problem(w, 400, "Invalid template", "Provide a ServiceTemplate YAML document of at most 256 KiB.")
		return false
	}
	docs, err := workflow.Parse("template.yaml", []byte(input.Document))
	if err != nil {
		problem(w, 400, "Invalid template", err.Error())
		return false
	}
	if len(docs) != 1 || docs[0].ServiceTemplate == nil {
		problem(w, 400, "Invalid template", "Provide exactly one ServiceTemplate document.")
		return false
	}
	if input.ConfigSourceID != "" {
		source, err := a.store.GetConfigSource(r.Context(), input.ConfigSourceID)
		if err != nil || source.ProjectID != item.ProjectID {
			problem(w, 400, "Invalid repository connection", "Choose a repository configuration in this project.")
			return false
		}
	} else if len(docs[0].ServiceTemplate.Sources) > 0 {
		problem(w, 400, "Repository connection required", "Choose a repository configuration to fetch the template's sources.")
		return false
	}
	document, err := docs[0].MarshalYAML()
	if err != nil {
		a.internal(w, err)
		return false
	}
	digest, err := docs[0].Digest()
	if err != nil {
		a.internal(w, err)
		return false
	}
	item.Name, item.Document, item.Digest, item.ConfigSourceID = docs[0].Metadata.Name, string(document), digest, input.ConfigSourceID
	return true
}

func (a *API) createServiceTemplate(w http.ResponseWriter, r *http.Request) {
	var input serviceTemplateRequest
	if !decode(w, r, &input) {
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectConfigure, input.ProjectID) {
		return
	}
	if _, err := a.store.GetProject(r.Context(), input.ProjectID); err != nil {
		a.notFoundOrInternal(w, err, "Project")
		return
	}
	now := time.Now().UTC()
	item := core.SavedServiceTemplate{ID: ulid.Make().String(), ProjectID: input.ProjectID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if !a.serviceTemplateInput(w, r, input, &item) {
		return
	}
	if err := a.store.CreateSavedServiceTemplate(r.Context(), item); err != nil {
		a.serviceTemplateWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) editableServiceTemplate(w http.ResponseWriter, r *http.Request) (core.SavedServiceTemplate, bool) {
	resource, project, revision, err := a.serviceTemplateResource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Service template")
		return core.SavedServiceTemplate{}, false
	}
	if !a.requireProject(w, r, core.PermissionProjectConfigure, project) {
		return core.SavedServiceTemplate{}, false
	}
	if revision == 0 {
		problem(w, 409, "Repository template", "Edit or remove this template in its source repository.")
		return core.SavedServiceTemplate{}, false
	}
	item, err := a.store.GetSavedServiceTemplate(r.Context(), resource.ID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Service template")
		return item, false
	}
	return item, true
}

func (a *API) updateServiceTemplate(w http.ResponseWriter, r *http.Request) {
	item, ok := a.editableServiceTemplate(w, r)
	if !ok {
		return
	}
	var input serviceTemplateRequest
	if !decode(w, r, &input) {
		return
	}
	if input.ProjectID != item.ProjectID {
		problem(w, 400, "Project cannot change", "Create a new template to use another project.")
		return
	}
	if input.Revision != item.Revision {
		a.serviceTemplateWriteError(w, store.ErrServiceTemplateConflict)
		return
	}
	if !a.serviceTemplateInput(w, r, input, &item) {
		return
	}
	item.Revision++
	item.UpdatedAt = time.Now().UTC()
	if err := a.store.UpdateSavedServiceTemplate(r.Context(), item, input.Revision); err != nil {
		a.serviceTemplateWriteError(w, err)
		return
	}
	writeJSON(w, 200, item)
}

func (a *API) deleteServiceTemplate(w http.ResponseWriter, r *http.Request) {
	item, ok := a.editableServiceTemplate(w, r)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	if err != nil || revision < 1 {
		problem(w, 400, "Revision required", "Reload the template before deleting it.")
		return
	}
	if err := a.store.DeleteSavedServiceTemplate(r.Context(), item.ID, revision); err != nil {
		a.serviceTemplateWriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) serviceTemplateWriteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrAlreadyExists):
		problem(w, 409, "Template already exists", "Choose a unique template name in this project.")
	case errors.Is(err, store.ErrServiceTemplateConflict):
		problem(w, 409, "Template changed", "Reload the template and try again.")
	default:
		a.internal(w, err)
	}
}
