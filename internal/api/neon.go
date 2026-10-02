package api

import (
	"context"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/neon"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
	"net/http"
	"strings"
	"time"
)

func (a *API) neonRoutes(r chi.Router) {
	r.Route("/neon-providers", func(r chi.Router) {
		r.Get("/", a.listNeonProviders)
		r.With(a.ownerOnly).Post("/", a.createNeonProvider)
		r.Route("/{id}", func(r chi.Router) {
			r.Use(a.ownerOnly)
			a.destructiveRoute(r, "DELETE", "/", "neon-provider", "delete", a.deleteNeonProvider)
		})
	})
}
func (a *API) createNeonProvider(w http.ResponseWriter, r *http.Request) {
	var p core.NeonProvider
	if !decode(w, r, &p) {
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectConfigure, p.ProjectID) {
		return
	}
	if _, err := a.store.GetProject(r.Context(), p.ProjectID); err != nil {
		a.notFoundOrInternal(w, err, "Project")
		return
	}
	if p.Endpoint == "" {
		p.Endpoint = neon.DefaultEndpoint
	}
	p.Name = strings.TrimSpace(p.Name)
	spec := neon.Spec{Endpoint: p.Endpoint, ProjectID: p.NeonProjectID, ParentBranchID: p.ParentBranchID, CredentialRef: p.CredentialRef, Database: "neondb"}
	if err := neon.ValidateSpec(spec); err != nil || p.Name == "" || len(p.Name) > 100 {
		problem(w, 400, "Invalid Neon provider", "Provide a name, HTTPS endpoint, project, parent branch and credential reference.")
		return
	}
	secret, err := a.store.GetSecret(r.Context(), p.CredentialRef)
	if err != nil || core.PlainSecretType(secret.Type) {
		problem(w, 400, "Invalid Neon credential", "Select an encrypted API credential scoped to this Neon project.")
		return
	}
	p.ID, p.CreatedAt = ulid.Make().String(), time.Now().UTC()
	if err = a.store.(store.NeonStore).CreateNeonProvider(r.Context(), p); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 201, p)
}
func (a *API) listNeonProviders(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("projectId")
	if !a.requireProject(w, r, core.PermissionProjectView, project) {
		return
	}
	items, err := a.store.(store.NeonStore).ListNeonProviders(r.Context(), project)
	if err != nil {
		a.internal(w, err)
		return
	}
	if currentIdentity(r.Context()).SystemRole != core.UserRoleOwner {
		for i := range items {
			items[i].CredentialRef = ""
		}
	}
	writeJSON(w, 200, items)
}
func (a *API) resolveNeonSpec(ctx context.Context, project string, n core.NeonServiceProvision) (neon.Spec, error) {
	p, err := a.store.(store.NeonStore).GetNeonProvider(ctx, n.ProviderRef)
	if err != nil || p.ProjectID != project {
		return neon.Spec{}, errors.New("choose a Neon provider assigned to this project")
	}
	s := neon.Spec{Endpoint: p.Endpoint, ProjectID: p.NeonProjectID, ParentBranchID: p.ParentBranchID, CredentialRef: p.CredentialRef, Database: n.Database, DataMode: n.DataMode, SuspendAfterSeconds: n.SuspendAfterSeconds}
	return s, neon.ValidateSpec(s)
}

type neonDataCopyApprovalKey struct{}

type acceptedNeonResource struct {
	Spec  neon.Spec
	Scope neon.Scope
	Token string
}

func (a *API) neonClient(r acceptedServiceResource) (*neon.Client, error) {
	if r.Neon == nil {
		return nil, errors.New("Neon accepted request unavailable")
	}
	return neon.New(r.Neon.Spec, r.Neon.Token, a.neonHTTPClient)
}
func (a *API) inspectNeonResource(ctx context.Context, r acceptedServiceResource) (core.ServiceResourceInspection, error) {
	result := core.ServiceResourceInspection{RunID: r.Request.Run.ID, ProjectID: r.Request.Run.ProjectID, Provider: "neon", StorageRetained: false}
	client, err := a.neonClient(r)
	if err != nil {
		return result, err
	}
	value, err := client.Inspect(ctx, r.Neon.Spec, r.Neon.Scope)
	result.ResourceID, result.State = value.Branch.ID, value.State
	return result, err
}
func (a *API) runNeonResource(ctx context.Context, record *core.ServiceResource, r acceptedServiceResource, retry bool) (map[string]string, error) {
	client, err := a.neonClient(r)
	if err != nil {
		return nil, err
	}
	progress := func(ctx context.Context, phase string, value neon.Resource) error {
		record.ProviderPhase = phase
		if phase == "Creating isolated Neon branch" {
			record.ProviderCreateAttempted = true
		}
		if value.Branch.ID != "" {
			record.ResourceID = value.Branch.ID
		}
		return a.store.(store.NeonStore).CheckpointNeonResource(ctx, *record)
	}
	value, err := client.Ensure(ctx, r.Neon.Spec, r.Neon.Scope, retry && !record.ProviderCreateAttempted, progress)
	if err != nil {
		return nil, err
	}
	record.ResourceID = value.Branch.ID
	return client.Connection(ctx, r.Neon.Spec, r.Neon.Scope, value.Branch.ID)
}

func (a *API) deleteNeonProvider(w http.ResponseWriter, r *http.Request) {
	p, err := a.store.(store.NeonStore).GetNeonProvider(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Provider")
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectConfigure, p.ProjectID) {
		return
	}
	if err = a.store.(store.NeonStore).DeleteUnusedNeonProvider(r.Context(), p.ID); err != nil {
		problem(w, 409, "Provider in use", "Remove template references first. Owned service history keeps its provider registration for recovery.")
		return
	}
	destructiveOutcome(r, "succeeded")
	w.WriteHeader(204)
}
