package api

import (
	"context"
	"database/sql"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"net/http"

	"github.com/doout/dispatch/internal/provision"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

func (a *API) infrastructureManager() *provision.Manager {
	data, ok := a.store.(provision.ProviderStore)
	if !ok {
		return nil
	}
	quota, ok := a.store.(interface {
		InfrastructureQuotaAdmission(context.Context, *sql.Tx, core.InfrastructureAcceptance) error
	})
	if !ok {
		return nil
	}
	return &provision.Manager{RequireRelay: a.auth.Hosted != nil, Store: data, Secrets: a.secretResolver, Edge: a.edge, Vault: a.eventConfig.Vault, Admission: quota.InfrastructureQuotaAdmission, Authorize: a.authorizeInfrastructure, Bootstrap: a.bootstrapManager()}
}
func (a *API) infrastructureRoutes(r chi.Router) {
	a.infrastructureLifecycleRoutes(r)
	a.infrastructureSnapshotRoutes(r)
	a.targetBootstrapRoutes(r)
	r.Route("/infrastructure/providers", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Get("/", a.listInfrastructureProviders)
		r.Post("/", a.createInfrastructureProvider)
		r.Route("/{id}", func(r chi.Router) {
			r.Put("/", a.updateInfrastructureProvider)
			r.Post("/verify", a.verifyInfrastructureProvider)
			r.Post("/options", a.infrastructureProviderOptions)
		})
	})
}
func (a *API) infrastructureProblem(w http.ResponseWriter, err error) {
	if errors.Is(err, errInfrastructureDenied) {
		problem(w, 403, "Infrastructure access denied", "The current project permission and provider assignment are required.")
		return
	}
	if infrastructureQuotaProblem(w, err) {
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		problem(w, 404, "Provider not found", "Choose an existing provider registration.")
		return
	}
	if errors.Is(err, store.ErrSnapshotProtected) || errors.Is(err, store.ErrInfrastructureChanged) || errors.Is(err, store.ErrInfrastructureProtected) || errors.Is(err, store.ErrStorageProtected) || errors.Is(err, store.ErrProviderChanged) || errors.Is(err, provision.ErrDisabled) || errors.Is(err, provision.ErrIdentity) {
		problem(w, 409, "Provider review required", err.Error())
		return
	}
	// Manager errors are deliberately fixed descriptions. Store/network errors
	// are not returned to callers because upstream details may contain secrets.
	problem(w, 422, "Provider request rejected", "Check the endpoint, enrolled route, secret reference, approved capabilities and current registration revision.")
}
func (a *API) listInfrastructureProviders(w http.ResponseWriter, r *http.Request) {
	manager := a.infrastructureManager()
	if manager == nil {
		problem(w, 503, "Provider storage unavailable", "Provider registrations require durable storage.")
		return
	}
	items, err := manager.Store.ListInfrastructureProviders(r.Context())
	w.Header().Set("Cache-Control", "no-store")
	a.list(w, items, err)
}
func (a *API) createInfrastructureProvider(w http.ResponseWriter, r *http.Request) {
	manager := a.infrastructureManager()
	if manager == nil {
		problem(w, 503, "Provider storage unavailable", "Provider registrations require durable storage.")
		return
	}
	var in provision.Registration
	if !decode(w, r, &in) {
		return
	}
	item, err := manager.Register(r.Context(), in)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 201, item)
}
func (a *API) updateInfrastructureProvider(w http.ResponseWriter, r *http.Request) {
	manager := a.infrastructureManager()
	if manager == nil {
		problem(w, 503, "Provider storage unavailable", "Provider registrations require durable storage.")
		return
	}
	var in provision.Registration
	if !decode(w, r, &in) {
		return
	}
	item, err := manager.Update(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (a *API) verifyInfrastructureProvider(w http.ResponseWriter, r *http.Request) {
	manager := a.infrastructureManager()
	if manager == nil {
		problem(w, 503, "Provider storage unavailable", "Provider registrations require durable storage.")
		return
	}
	var in struct {
		Revision int64 `json:"revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	item, err := manager.Verify(r.Context(), chi.URLParam(r, "id"), in.Revision)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (a *API) infrastructureProviderOptions(w http.ResponseWriter, r *http.Request) {
	manager := a.infrastructureManager()
	if manager == nil {
		problem(w, 503, "Provider storage unavailable", "Provider registrations require durable storage.")
		return
	}
	var in struct {
		Kind   string         `json:"kind"`
		Config map[string]any `json:"config"`
	}
	if !decode(w, r, &in) {
		return
	}
	items, err := manager.Options(r.Context(), chi.URLParam(r, "id"), in.Kind, in.Config)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, items)
}

func (a *API) providerCredentialUnused(w http.ResponseWriter, r *http.Request, id string) bool {
	manager := a.infrastructureManager()
	if manager == nil {
		return true
	}
	items, err := manager.Store.ListInfrastructureProviders(r.Context())
	if err != nil {
		a.internal(w, err)
		return false
	}
	for _, item := range items {
		if item.CredentialSecretID == id {
			problem(w, 409, "Credential in use", "Replace the credential reference on the infrastructure provider before deleting it or changing its type.")
			return false
		}
	}
	return true
}

var errInfrastructureDenied = errors.New("project infrastructure permission denied")

func (a *API) authorizeInfrastructure(ctx context.Context, project, provider, permission string) error {
	allowed, err := a.canProject(ctx, core.Permission(permission), project)
	if err != nil {
		return err
	}
	if !allowed {
		return errInfrastructureDenied
	}
	assigned, err := a.assignedInfrastructure(ctx, project, "provider", provider)
	if err != nil {
		return err
	}
	if !assigned {
		return errInfrastructureDenied
	}
	return nil
}
