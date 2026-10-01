package api

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

func (a *API) automationRoutes(r chi.Router) {
	r.Route("/automation-accounts", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Get("/", a.listAutomationAccounts)
		r.Post("/", a.createAutomationAccount)
		r.Route("/{id}", func(r chi.Router) {
			r.Put("/", a.updateAutomationAccount)
			r.Get("/credentials", a.listAutomationCredentials)
			r.Post("/credentials", a.issueAutomationCredential)
			r.Post("/credentials/{credentialId}/rotate", a.issueAutomationCredential)
			r.Post("/credentials/{credentialId}/revoke", a.revokeAutomationCredential)
		})
	})
	r.Route("/infrastructure/grants", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Get("/", a.listPrincipalGrants)
		r.Put("/", a.savePrincipalGrant)
		r.Delete("/{kind}/{principalId}/{projectId}", a.deletePrincipalGrant)
	})
	r.Route("/infrastructure/assignments/{projectId}", func(r chi.Router) {
		r.Get("/", a.listInfrastructureAssignments)
		r.With(a.ownerOnly).Put("/", a.saveInfrastructureAssignment)
		r.With(a.ownerOnly).Delete("/{kind}/{resourceId}", a.deleteInfrastructureAssignment)
	})
}
func (a *API) automationStore(w http.ResponseWriter) (store.AutomationStore, bool) {
	d, ok := a.store.(store.AutomationStore)
	if !ok {
		problem(w, 503, "Automation unavailable", "Durable automation identity storage is required.")
	}
	w.Header().Set("Cache-Control", "no-store")
	return d, ok
}
func (a *API) listAutomationAccounts(w http.ResponseWriter, r *http.Request) {
	d, ok := a.automationStore(w)
	if !ok {
		return
	}
	items, err := d.ListServiceAccounts(r.Context())
	a.list(w, items, err)
}
func (a *API) createAutomationAccount(w http.ResponseWriter, r *http.Request) {
	d, ok := a.automationStore(w)
	if !ok {
		return
	}
	var in struct{ Name, Description string }
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 120 || len(in.Description) > 2000 {
		problem(w, 422, "Invalid automation account", "Use a name of 1–120 bytes and a description up to 2000 bytes.")
		return
	}
	now := time.Now().UTC()
	item := core.ServiceAccount{ID: ulid.Make().String(), Name: in.Name, Description: in.Description, State: "active", CreatedAt: now, UpdatedAt: now}
	if err := d.CreateServiceAccount(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 201, item)
}
func (a *API) updateAutomationAccount(w http.ResponseWriter, r *http.Request) {
	d, ok := a.automationStore(w)
	if !ok {
		return
	}
	item, err := d.GetServiceAccount(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Automation account")
		return
	}
	var in struct{ Name, Description, State string }
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 120 || len(in.Description) > 2000 || (in.State != "active" && in.State != "disabled") {
		problem(w, 422, "Invalid automation account", "Provide a name and an active or disabled state.")
		return
	}
	item.Name, item.Description, item.State, item.UpdatedAt = in.Name, in.Description, in.State, time.Now().UTC()
	if err = d.UpdateServiceAccount(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (a *API) listAutomationCredentials(w http.ResponseWriter, r *http.Request) {
	d, ok := a.automationStore(w)
	if !ok {
		return
	}
	if _, err := d.GetServiceAccount(r.Context(), chi.URLParam(r, "id")); err != nil {
		a.notFoundOrInternal(w, err, "Automation account")
		return
	}
	items, err := d.ListAutomationCredentials(r.Context(), chi.URLParam(r, "id"))
	a.list(w, items, err)
}
func (a *API) issueAutomationCredential(w http.ResponseWriter, r *http.Request) {
	d, ok := a.automationStore(w)
	if !ok {
		return
	}
	var in struct {
		Name      string    `json:"name"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if !decode(w, r, &in) {
		return
	}
	now := time.Now().UTC()
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 120 || !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(365*24*time.Hour)) {
		problem(w, 422, "Invalid credential", "Provide a name and an expiry within the next 365 days.")
		return
	}
	entropy := make([]byte, 32)
	if _, err := rand.Read(entropy); err != nil {
		a.internal(w, err)
		return
	}
	c := core.AutomationCredential{ID: ulid.Make().String(), AccountID: chi.URLParam(r, "id"), Name: in.Name, CreatedAt: now, ExpiresAt: in.ExpiresAt.UTC()}
	token := "dsa_" + c.ID + "_" + base64.RawURLEncoding.EncodeToString(entropy)
	c.TokenHash = sessionHash(token)
	if err := d.IssueAutomationCredential(r.Context(), c, chi.URLParam(r, "credentialId")); err != nil {
		if errors.Is(err, store.ErrAutomationCredential) {
			problem(w, 409, "Credential unavailable", "The account or credential is inactive. Reload its current state before issuing a token.")
			return
		}
		a.internal(w, err)
		return
	}
	c.TokenHash = ""
	writeJSON(w, 201, map[string]any{"credential": c, "token": token})
}
func (a *API) revokeAutomationCredential(w http.ResponseWriter, r *http.Request) {
	d, ok := a.automationStore(w)
	if !ok {
		return
	}
	if err := d.RevokeAutomationCredential(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "credentialId"), time.Now().UTC()); err != nil {
		a.notFoundOrInternal(w, err, "Credential")
		return
	}
	w.WriteHeader(204)
}
func (a *API) listPrincipalGrants(w http.ResponseWriter, r *http.Request) {
	d, ok := a.automationStore(w)
	if !ok {
		return
	}
	items, err := d.ListPrincipalGrants(r.Context(), "", "")
	a.list(w, items, err)
}
func (a *API) savePrincipalGrant(w http.ResponseWriter, r *http.Request) {
	d, ok := a.automationStore(w)
	if !ok {
		return
	}
	var in core.PrincipalGrant
	if !decode(w, r, &in) {
		return
	}
	if _, err := a.store.GetProject(r.Context(), in.ProjectID); err != nil {
		problem(w, 422, "Invalid project", "Choose an existing project.")
		return
	}
	valid := false
	switch in.PrincipalType {
	case core.PrincipalUser:
		u, err := a.store.GetUser(r.Context(), in.PrincipalID)
		valid = err == nil && u.ID != ""
	case core.PrincipalServiceAccount:
		v, err := d.GetServiceAccount(r.Context(), in.PrincipalID)
		valid = err == nil && v.ID != ""
	}
	if !valid || len(in.Permissions) == 0 || len(in.Permissions) > len(core.AssignableProjectPermissions()) || in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()) {
		problem(w, 422, "Invalid grant", "Choose an existing user or automation account, permissions, and a future expiry if supplied.")
		return
	}
	allowed := map[core.Permission]bool{}
	for _, p := range core.AssignableProjectPermissions() {
		allowed[p] = true
	}
	seen := map[core.Permission]bool{}
	for _, p := range in.Permissions {
		if !allowed[p] || seen[p] {
			problem(w, 422, "Invalid permission", "Use each supported project or infrastructure permission at most once. Human approval and controller administration cannot be granted here.")
			return
		}
		seen[p] = true
	}
	in.UpdatedAt = time.Now().UTC()
	if err := d.SavePrincipalGrant(r.Context(), in); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, in)
}
func (a *API) deletePrincipalGrant(w http.ResponseWriter, r *http.Request) {
	d, ok := a.automationStore(w)
	if !ok {
		return
	}
	if err := d.DeletePrincipalGrant(r.Context(), chi.URLParam(r, "kind"), chi.URLParam(r, "principalId"), chi.URLParam(r, "projectId")); err != nil {
		a.internal(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *API) listInfrastructureAssignments(w http.ResponseWriter, r *http.Request) {
	d, ok := a.automationStore(w)
	if !ok {
		return
	}
	project := chi.URLParam(r, "projectId")
	if !a.requireProject(w, r, core.PermissionInfrastructureInspect, project) {
		return
	}
	items, err := d.ListInfrastructureAssignments(r.Context(), project)
	a.list(w, items, err)
}
func (a *API) saveInfrastructureAssignment(w http.ResponseWriter, r *http.Request) {
	d, ok := a.automationStore(w)
	if !ok {
		return
	}
	var in core.InfrastructureAssignment
	if !decode(w, r, &in) {
		return
	}
	in.ProjectID = chi.URLParam(r, "projectId")
	if _, err := a.store.GetProject(r.Context(), in.ProjectID); err != nil {
		problem(w, 422, "Invalid project", "Choose an existing project.")
		return
	}
	valid := false
	switch in.Kind {
	case "provider":
		if ps, ok := a.store.(store.InfrastructureProviderStore); ok {
			_, err := ps.GetInfrastructureProvider(r.Context(), in.ResourceID)
			valid = err == nil
		}
	case "target":
		target, err := a.store.GetServer(r.Context(), in.ResourceID)
		valid = err == nil && (target.ProjectID == "" || target.ProjectID == in.ProjectID)
	}
	if !valid {
		problem(w, 422, "Invalid assignment", "Choose an existing provider or a target that is unowned or owned by this project.")
		return
	}
	in.UpdatedAt = time.Now().UTC()
	if err := d.SaveInfrastructureAssignment(r.Context(), in); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, in)
}
func (a *API) deleteInfrastructureAssignment(w http.ResponseWriter, r *http.Request) {
	d, ok := a.automationStore(w)
	if !ok {
		return
	}
	if err := d.DeleteInfrastructureAssignment(r.Context(), chi.URLParam(r, "projectId"), chi.URLParam(r, "kind"), chi.URLParam(r, "resourceId")); err != nil {
		a.internal(w, err)
		return
	}
	w.WriteHeader(204)
}
