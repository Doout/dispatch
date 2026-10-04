package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/bootstrap"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

func (a *API) bootstrapManager() *bootstrap.Manager {
	a.bootstrapOnce.Do(func() {
		if a.eventConfig.Bootstrap != nil {
			a.bootstrap = a.eventConfig.Bootstrap
			return
		}
		if data, ok := a.store.(interface {
			bootstrap.Store
			store.InfrastructureLifecycleStore
		}); ok && a.eventConfig.Vault != nil {
			a.bootstrap = bootstrap.Configured(data, a.eventConfig.Vault, a.auth.PublicURL)
		}
	})
	return a.bootstrap
}
func (a *API) bootstrapAvailable(w http.ResponseWriter) *bootstrap.Manager {
	m := a.bootstrapManager()
	if m == nil {
		problem(w, 503, "Target bootstrap unavailable", "Encrypted durable bootstrap storage is required.")
	}
	return m
}
func (a *API) bootstrapProblem(w http.ResponseWriter, err error) {
	status := 422
	detail := "Check the reviewed artifact, verified SSH key, target ownership and current installation state."
	if errors.Is(err, store.ErrNotFound) {
		status = 404
		detail = "The installation review does not exist."
	}
	if errors.Is(err, bootstrap.ErrCredential) {
		status = 401
		detail = "The installation claim is invalid or expired."
	}
	if errors.Is(err, bootstrap.ErrWaiting) {
		status = 409
		detail = "Waiting for the accepted provider resource to be allocated."
	}
	if errors.Is(err, bootstrap.ErrConflict) || errors.Is(err, store.ErrInfrastructureChanged) {
		status = 409
		detail = "The installation or target identity changed; review it again."
	}
	problem(w, status, "Bootstrap request unavailable", detail)
}
func (a *API) targetBootstrapRoutes(r chi.Router) {
	r.Route("/infrastructure/bootstrap", func(r chi.Router) {
		r.Get("/", a.listTargetBootstraps)
		r.With(a.ownerOnly).Post("/review", a.reviewTargetBootstrap)
		r.Get("/{id}", a.getTargetBootstrap)
		r.With(a.ownerOnly).Post("/{id}/accept", a.acceptTargetBootstrap)
		r.Post("/{id}/retry", a.retryTargetBootstrap)
	})
}

// Project callers may inspect and resume an existing accepted installation;
// importing a machine and approving a new SSH plan remain owner operations.
func (a *API) authorizeTargetBootstrap(ctx context.Context, item core.TargetBootstrap, permission core.Permission) error {
	if currentIdentity(ctx).SystemRole == core.UserRoleOwner {
		return nil
	}
	if item.ProjectID == "" {
		return errInfrastructureDenied
	}
	allowed, err := a.canProject(ctx, permission, item.ProjectID)
	if err != nil {
		return err
	}
	if !allowed {
		return errInfrastructureDenied
	}
	if item.ProviderID != "" {
		if err := a.authorizeInfrastructure(ctx, item.ProjectID, item.ProviderID, string(permission)); err != nil {
			return err
		}
		data, ok := a.store.(store.InfrastructureLifecycleStore)
		if !ok {
			return errInfrastructureDenied
		}
		server, err := data.GetManagedServer(ctx, item.ServerID)
		if err != nil {
			return err
		}
		if server.ProjectID != item.ProjectID || server.ProviderID != item.ProviderID || server.NodeID != item.NodeID {
			return errInfrastructureDenied
		}
		return nil
	}
	server, err := a.store.GetServer(ctx, item.ServerID)
	if err != nil {
		return err
	}
	if server.ProjectID != item.ProjectID {
		return errInfrastructureDenied
	}
	assigned, err := a.assignedInfrastructure(ctx, item.ProjectID, "target", item.ServerID)
	if err != nil {
		return err
	}
	if !assigned {
		return errInfrastructureDenied
	}
	return nil
}
func (a *API) listTargetBootstraps(w http.ResponseWriter, r *http.Request) {
	m := a.bootstrapAvailable(w)
	if m == nil {
		return
	}
	items, err := m.Store.ListTargetBootstraps(r.Context(), r.URL.Query().Get("serverId"))
	if err != nil {
		a.bootstrapProblem(w, err)
		return
	}
	visible := make([]core.TargetBootstrap, 0, len(items))
	for _, item := range items {
		if project := r.URL.Query().Get("projectId"); project != "" && item.ProjectID != project {
			continue
		}
		if err := a.authorizeTargetBootstrap(r.Context(), item, core.PermissionInfrastructureInspect); errors.Is(err, errInfrastructureDenied) || errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			a.internal(w, err)
			return
		}
		visible = append(visible, item)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, visible)
}
func (a *API) getTargetBootstrap(w http.ResponseWriter, r *http.Request) {
	m := a.bootstrapAvailable(w)
	if m == nil {
		return
	}
	item, err := m.Store.GetTargetBootstrap(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.bootstrapProblem(w, err)
		return
	}
	if err := a.authorizeTargetBootstrap(r.Context(), item, core.PermissionInfrastructureInspect); err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	item, err = m.Refresh(r.Context(), item.ID)
	if err != nil {
		a.bootstrapProblem(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, item)
}
func (a *API) reviewTargetBootstrap(w http.ResponseWriter, r *http.Request) {
	m := a.bootstrapAvailable(w)
	if m == nil {
		return
	}
	var in struct {
		ServerID    string                   `json:"serverId"`
		Plan        core.TargetBootstrapPlan `json:"plan"`
		Credentials bootstrap.SSHCredentials `json:"credentials"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Plan.Method != "ssh" {
		problem(w, 422, "Verified SSH required", "Review provider cloud-init with the machine creation request.")
		return
	}
	binding := bootstrap.Binding{}
	if in.ServerID == "" {
		binding.ServerID = ulid.Make().String()
		binding.NodeID = "node-" + binding.ServerID
	} else {
		binding.ServerID = in.ServerID
		manager := a.infrastructureManager()
		if manager == nil {
			problem(w, 503, "Target storage unavailable", "Durable target ownership is required.")
			return
		}
		data := manager.Store.(store.InfrastructureLifecycleStore)
		managed, err := data.GetManagedServer(r.Context(), in.ServerID)
		if err == nil {
			if managed.AllocationState != "allocated" || managed.Address != in.Plan.SSHHost {
				a.bootstrapProblem(w, bootstrap.ErrConflict)
				return
			}
			binding = bootstrap.Binding{ServerID: managed.ID, NodeID: managed.NodeID, ReviewID: managed.ReviewID, ProviderID: managed.ProviderID, ProjectID: managed.ProjectID, ResourceID: managed.ResourceID, Accepted: true}
			in.Plan.TargetName = managed.Name
		} else if errors.Is(err, store.ErrNotFound) {
			target, e := a.store.GetServer(r.Context(), in.ServerID)
			if e != nil {
				a.bootstrapProblem(w, e)
				return
			}
			if target.Runtime != core.ServerRuntimeDocker || target.Address == "local" || target.Address != in.Plan.SSHHost {
				a.bootstrapProblem(w, bootstrap.ErrConflict)
				return
			}
			binding.NodeID = target.AgentNodeID
			if binding.NodeID == "" {
				binding.NodeID = "node-" + target.ID
			}
			binding.ProjectID = target.ProjectID
			in.Plan.TargetName = target.Name
		} else {
			a.bootstrapProblem(w, err)
			return
		}
	}
	item, _, err := m.Prepare(r.Context(), binding, in.Plan, in.Credentials, currentIdentity(r.Context()).ID)
	if err != nil {
		a.bootstrapProblem(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, item)
}
func (a *API) acceptTargetBootstrap(w http.ResponseWriter, r *http.Request) {
	m := a.bootstrapAvailable(w)
	if m == nil {
		return
	}
	var in struct {
		Digest      string `json:"digest"`
		ConfirmName string `json:"confirmName"`
	}
	if !decode(w, r, &in) {
		return
	}
	item, err := m.Store.GetTargetBootstrap(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.bootstrapProblem(w, err)
		return
	}
	if item.Plan.Method != "ssh" || in.ConfirmName != item.Plan.TargetName || currentIdentity(r.Context()).Kind != core.PrincipalUser {
		problem(w, 403, "Installation approval required", "A controller owner must confirm this SSH installation and target name.")
		return
	}
	item, err = m.Accept(r.Context(), item.ID, in.Digest)
	if err != nil {
		a.bootstrapProblem(w, err)
		return
	}
	writeJSON(w, 202, item)
}
func (a *API) retryTargetBootstrap(w http.ResponseWriter, r *http.Request) {
	m := a.bootstrapAvailable(w)
	if m == nil {
		return
	}
	var in struct {
		Digest string `json:"digest"`
	}
	if !decode(w, r, &in) {
		return
	}
	item, err := m.Store.GetTargetBootstrap(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.bootstrapProblem(w, err)
		return
	}
	if err := a.authorizeTargetBootstrap(r.Context(), item, core.PermissionInfrastructureModify); err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	item, err = m.Retry(r.Context(), item.ID, in.Digest)
	if err != nil {
		a.bootstrapProblem(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 202, item)
}
func claimBearer(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(value, "Bearer ")
}
func (a *API) claimTargetBootstrap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	m := a.bootstrapAvailable(w)
	if m == nil {
		return
	}
	item, err := m.Claim(r.Context(), chi.URLParam(r, "id"), claimBearer(r))
	if err != nil {
		a.bootstrapProblem(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (a *API) progressTargetBootstrap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	m := a.bootstrapAvailable(w)
	if m == nil {
		return
	}
	var in struct {
		Phase string `json:"phase"`
	}
	if !decode(w, r, &in) {
		return
	}
	if _, err := m.Progress(r.Context(), chi.URLParam(r, "id"), claimBearer(r), in.Phase); err != nil {
		a.bootstrapProblem(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *API) bootstrapArtifact(w http.ResponseWriter, r *http.Request) {
	m := a.bootstrapAvailable(w)
	if m == nil {
		return
	}
	digest, raw, err := m.Artifact(chi.URLParam(r, "platform"))
	if err != nil || digest != chi.URLParam(r, "digest") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "dispatch-agent", time.Time{}, bytes.NewReader(raw))
}
