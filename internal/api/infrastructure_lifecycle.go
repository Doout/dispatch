package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provision"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

// Every manager action checks its current project grant and provider assignment.
// Provider registration and credential administration remain owner-only.
func (a *API) infrastructureLifecycleRoutes(r chi.Router) {
	r.Route("/infrastructure/servers", func(r chi.Router) {
		r.Get("/", a.listManagedServers)
		r.Post("/review", a.reviewManagedServer)
		r.Post("/", a.createManagedServer)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", a.getManagedServer)
			r.Get("/operations", a.managedServerOperations)
			r.Post("/power", a.powerManagedServer)
			r.Get("/clone", a.inspectManagedClone)
			r.Post("/promote", a.promoteManagedClone)
			r.Post("/snapshot-review", a.reviewInfrastructureSnapshot)
			r.Post("/enrollment", a.enrollManagedServer)
			r.Post("/adopt", a.adoptManagedServer)
			r.Post("/delete-review", a.reviewManagedServerDeletion)
			r.Post("/delete", a.deleteManagedServer)
		})
	})
	r.Post("/infrastructure/operations/{id}/{action}", a.changeInfrastructureOperation)
	r.Get("/projects/{id}/infrastructure/providers", a.projectInfrastructureCatalog)
	r.Post("/projects/{id}/infrastructure/providers/{providerId}/options", a.projectInfrastructureOptions)
}
func (a *API) lifecycleManager(w http.ResponseWriter) *provision.Manager {
	m := a.infrastructureManager()
	if m == nil {
		problem(w, 503, "Infrastructure unavailable", "Durable provider storage is required.")
	}
	return m
}
func (a *API) listManagedServers(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	items, err := m.ListManaged(r.Context())
	a.list(w, items, err)
}
func (a *API) getManagedServer(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	item, err := m.GetManaged(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, item)
}
func (a *API) reviewManagedServer(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	var in provision.CreateInput
	if !decode(w, r, &in) {
		return
	}
	if !a.requireInfrastructureCredentials(w, r, in.ProjectID, in.SSHKeySecretID, in.SecretRefs) {
		return
	}
	in.ActorID = currentIdentity(r.Context()).ID
	item, err := m.ReviewCreate(r.Context(), in)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, item)
}
func (a *API) createManagedServer(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	var in provision.Acceptance
	if !decode(w, r, &in) {
		return
	}
	data, ok := a.store.(store.InfrastructureLifecycleStore)
	if !ok {
		problem(w, 503, "Infrastructure unavailable", "Durable infrastructure storage is unavailable.")
		return
	}
	if !a.authorizeInfrastructureReview(w, r, m, in.ReviewID) {
		return
	}
	review, err := data.GetInfrastructureReview(r.Context(), in.ReviewID)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	if !a.authorizeInfrastructureMutationReceipt(w, r, review.ProjectID, review.ProviderID, core.PermissionInfrastructureCreate) {
		return
	}
	if review.SourceSnapshotID != "" && !a.authorizeInfrastructureMutationReceipt(w, r, review.ProjectID, review.ProviderID, core.PermissionSnapshotRestore) {
		return
	}
	action := "server.create"
	if review.SourceSnapshotID != "" {
		action = "server.restore"
	}
	r, receipt, proceed := a.reserveMutation(w, r, review.ProjectID, action, in, "infrastructure_operation", review.ServerID)
	if !proceed {
		return
	}
	item, err := m.AcceptCreate(r.Context(), currentIdentity(r.Context()).Kind+":"+currentIdentity(r.Context()).ID, in)
	if err != nil {
		a.rejectInfrastructureMutation(w, r, err)
		return
	}
	if a.completeInfrastructureMutation(w, r, receipt, item.Operation.ID) {
		return
	}
	writeJSON(w, 202, item)
}
func (a *API) managedServerOperations(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	items, err := m.Operations(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (a *API) changeInfrastructureOperation(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	if err := m.ChangeOperation(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "action")); err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *API) enrollManagedServer(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	item, err := m.Enrollment(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, item)
}
func (a *API) adoptManagedServer(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	var in provision.Adoption
	if !decode(w, r, &in) {
		return
	}
	item, err := m.Adopt(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (a *API) reviewManagedServerDeletion(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	id := chi.URLParam(r, "id")
	data, ok := a.store.(store.InfrastructureLifecycleStore)
	if !ok {
		problem(w, 503, "Infrastructure unavailable", "Durable lifecycle storage is required.")
		return
	}
	owned, err := data.GetManagedServer(r.Context(), id)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	if err := a.authorizeInfrastructure(r.Context(), owned.ProjectID, owned.ProviderID, "infrastructure.delete"); err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	if _, err := a.store.GetServer(r.Context(), id); err == nil {
		if err = a.refreshStorageReview(r.Context(), "server", id); err != nil {
			a.infrastructureProblem(w, err)
			return
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		a.infrastructureProblem(w, err)
		return
	}
	item, err := m.ReviewDelete(r.Context(), id)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (a *API) deleteManagedServer(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	var in provision.Acceptance
	if !decode(w, r, &in) {
		return
	}
	id := chi.URLParam(r, "id")
	data, ok := a.store.(store.InfrastructureLifecycleStore)
	if !ok {
		problem(w, 503, "Infrastructure unavailable", "Durable infrastructure storage is unavailable.")
		return
	}
	server, err := data.GetManagedServer(r.Context(), id)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	if !a.authorizeInfrastructureMutationReceipt(w, r, server.ProjectID, server.ProviderID, core.PermissionInfrastructureDelete) {
		return
	}
	r, receipt, proceed := a.reserveMutation(w, r, server.ProjectID, "server.delete", in, "infrastructure_operation", id)
	if !proceed {
		return
	}
	var item provision.Accepted
	remove := func() error {
		var err error
		item, err = m.Delete(r.Context(), id, currentIdentity(r.Context()).Kind+":"+currentIdentity(r.Context()).ID, in)
		return err
	}
	err = nil
	if server, e := a.store.GetServer(r.Context(), id); e == nil {
		err = a.deploy.Storage.WithTarget(r.Context(), id, func() error {
			if e := a.deploy.Storage.RefreshLocked(r.Context(), server); e != nil {
				return e
			}
			return remove()
		})
	} else if !errors.Is(e, store.ErrNotFound) {
		err = e
	} else {
		err = remove()
	}
	if err != nil {
		a.rejectInfrastructureMutation(w, r, err)
		return
	}
	if a.completeInfrastructureMutation(w, r, receipt, item.Operation.ID) {
		return
	}
	writeJSON(w, 202, item)
}
func (a *API) projectInfrastructureCatalog(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	items, err := m.Catalog(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (a *API) projectInfrastructureOptions(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	var in provision.ProjectOptionInput
	if !decode(w, r, &in) {
		return
	}
	if !a.requireCredentialOwner(w, r, len(in.SecretRefs) > 0) {
		return
	}
	items, err := m.ProjectOptions(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "providerId"), in)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 200, items)
}

// Receipt replay must pass the same current action and assignment checks as a
// fresh acceptance, even though it bypasses the provisioning manager.
func (a *API) authorizeInfrastructureMutationReceipt(w http.ResponseWriter, r *http.Request, project, providerID string, permission core.Permission) bool {
	if !a.requireProject(w, r, permission, project) {
		return false
	}
	assigned, err := a.assignedInfrastructure(r.Context(), project, "provider", providerID)
	if err != nil {
		a.internal(w, err)
		return false
	}
	if !assigned {
		problem(w, 403, "Provider access denied", "The provider is not assigned to this project.")
		return false
	}
	return true
}
func (a *API) rejectInfrastructureMutation(w http.ResponseWriter, r *http.Request, err error) {
	status := 422
	if errors.Is(err, errInfrastructureDenied) {
		status = 403
	}
	var quota *core.InfrastructureQuotaViolation
	if errors.As(err, &quota) && quota.Code != "allocation_disallowed" {
		status = 409
	}
	if errors.Is(err, store.ErrNotFound) {
		status = 404
	}
	if errors.Is(err, store.ErrSnapshotProtected) || errors.Is(err, store.ErrInfrastructureChanged) || errors.Is(err, store.ErrInfrastructureProtected) || errors.Is(err, store.ErrStorageProtected) || errors.Is(err, store.ErrProviderChanged) || errors.Is(err, provision.ErrDisabled) || errors.Is(err, provision.ErrIdentity) || errors.Is(err, store.ErrMutationClaimLost) {
		status = 409
	}
	a.failMutationAcceptance(r.Context(), status, "Infrastructure acceptance was rejected. Inspect the review and original operation before retrying.")
	if errors.Is(err, store.ErrMutationClaimLost) {
		problem(w, 409, "Acceptance changed", "Retry the identical request to inspect its original receipt.")
		return
	}
	a.infrastructureProblem(w, err)
}
func (a *API) completeInfrastructureMutation(w http.ResponseWriter, r *http.Request, receipt *core.MutationReceipt, id string) bool {
	core.RecordAcceptedOperation(r.Context(), id)
	if receipt == nil {
		return false
	}
	saved, err := a.store.(store.MutationReceiptStore).GetMutationReceipt(r.Context(), receipt.ID)
	if err != nil {
		a.internal(w, err)
		return true
	}
	a.writeMutationReceipt(w, r, saved, 202)
	return true
}

func (a *API) authorizeInfrastructureReview(w http.ResponseWriter, r *http.Request, m *provision.Manager, id string) bool {
	data, ok := m.Store.(store.InfrastructureLifecycleStore)
	if !ok {
		problem(w, 503, "Infrastructure unavailable", "Durable lifecycle storage is required.")
		return false
	}
	review, err := data.GetInfrastructureReview(r.Context(), id)
	if err != nil {
		a.infrastructureProblem(w, err)
		return false
	}
	if err = a.authorizeInfrastructure(r.Context(), review.ProjectID, review.ProviderID, "infrastructure.create"); err != nil {
		a.infrastructureProblem(w, err)
		return false
	}
	if review.SourceSnapshotID != "" {
		if err = a.authorizeInfrastructure(r.Context(), review.ProjectID, review.ProviderID, "infrastructure.restore"); err != nil {
			a.infrastructureProblem(w, err)
			return false
		}
	}
	var input provision.CreateInput
	if err = json.Unmarshal(review.Input, &input); err != nil {
		a.infrastructureProblem(w, err)
		return false
	}
	return a.requireInfrastructureCredentials(w, r, review.ProjectID, input.SSHKeySecretID, input.SecretRefs)
}

func (a *API) requireInfrastructureCredentials(w http.ResponseWriter, r *http.Request, project, sshKey string, refs map[string]string) bool {
	if currentIdentity(r.Context()).SystemRole == core.UserRoleOwner {
		return true
	}
	if len(refs) > 0 {
		problem(w, 403, "Credential access denied", "A controller owner must prepare provider configuration with global secret references.")
		return false
	}
	if sshKey != "" {
		assigned, err := a.assignedInfrastructure(r.Context(), project, "ssh_key", sshKey)
		if err != nil {
			a.internal(w, err)
			return false
		}
		if !assigned {
			problem(w, 403, "SSH public key unavailable", "An owner must assign this SSH public key to the project before it can be used for server creation.")
			return false
		}
	}
	return true
}
