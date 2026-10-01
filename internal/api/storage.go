package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

func (a *API) storageRoutes(r chi.Router) {
	r.Get("/storage", a.listStorage)
	r.Route("/storage/{id}", func(r chi.Router) {
		r.With(a.storagePermission(core.PermissionProjectView)).Get("/", a.getStorage)
		r.Group(func(r chi.Router) {
			r.Use(a.storagePermission(core.PermissionProjectConfigure))
			r.Put("/policy", a.setStoragePolicy)
			a.destructiveRoute(r, "DELETE", "/", "storage", "delete", a.deleteStorage)
		})
	})
}

func (a *API) storagePermission(permission core.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			item, err := a.store.GetStorage(r.Context(), chi.URLParam(r, "id"))
			if err != nil {
				a.notFoundOrInternal(w, err, "Storage")
				return
			}
			if item.ProjectID == "" {
				if !a.requireControllerOwner(w, r) {
					return
				}
			} else if !a.requireProject(w, r, permission, item.ProjectID) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (a *API) listStorage(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("projectId"))
	if project != "" && !a.requireProject(w, r, core.PermissionProjectView, project) {
		return
	}
	items, err := a.store.ListStorage(r.Context(), strings.TrimSpace(r.URL.Query().Get("serverId")))
	if err != nil {
		a.internal(w, err)
		return
	}
	visible, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	owner := currentIdentity(r.Context()).SystemRole == core.UserRoleOwner
	result := []core.StorageResource{}
	for _, item := range items {
		if project != "" && project != item.ProjectID {
			continue
		}
		if owner || item.ProjectID != "" && visible[item.ProjectID] {
			result = append(result, storageForIdentity(r.Context(), item))
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

func (a *API) getStorage(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetStorage(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Storage")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, storageForIdentity(r.Context(), item))
}

// Shared storage can have consumers outside the viewer's projects. Only the
// controller owner receives their runtime names and paths; others retain counts
// and active state for understanding deletion guards.
func storageForIdentity(ctx context.Context, item core.StorageResource) core.StorageResource {
	if currentIdentity(ctx).SystemRole == core.UserRoleOwner {
		return item
	}
	item.Consumers = append([]core.StorageConsumer{}, item.Consumers...)
	for i := range item.Consumers {
		item.Consumers[i].ID = fmt.Sprintf("consumer-%d", i+1)
		item.Consumers[i].Mount = ""
	}
	item.Mounts = []string{}
	return item
}

func (a *API) reconcileStorage(w http.ResponseWriter, r *http.Request) {
	server, err := a.store.GetServer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	if a.deploy.Storage.Backend == nil {
		problem(w, 409, "Inspection unavailable", "Runtime storage inspection is unavailable in simulation mode.")
		return
	}
	if err = a.deploy.Storage.Refresh(r.Context(), server); err != nil {
		problem(w, 409, "Storage inspection failed", err.Error())
		return
	}
	items, err := a.store.ListStorage(r.Context(), server.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, items)
}

func (a *API) setStoragePolicy(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision int64  `json:"revision"`
		Policy   string `json:"policy"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Revision < 1 || input.Policy != "retain" && input.Policy != "destroy" {
		problem(w, 422, "Invalid storage policy", "Provide the current revision and either retain or destroy.")
		return
	}
	item, err := a.store.GetStorage(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Storage")
		return
	}
	err = a.deploy.Storage.WithTarget(r.Context(), item.ServerID, func() error { return a.store.SetStoragePolicy(r.Context(), item.ID, input.Revision, input.Policy) })
	if err != nil {
		problem(w, 409, "Storage policy unchanged", err.Error())
		return
	}
	item, err = a.store.GetStorage(r.Context(), item.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, storageForIdentity(r.Context(), item))
}

func (a *API) deleteStorage(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetStorage(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Storage")
		return
	}
	err = a.deploy.Storage.WithTarget(r.Context(), item.ServerID, func() error {
		server, err := a.store.GetServer(r.Context(), item.ServerID)
		if err != nil {
			return err
		}
		if err = a.deploy.Storage.RefreshLocked(r.Context(), server); err != nil {
			return err
		}
		if err = a.recheckDestructiveAction(r, "storage", "delete"); err != nil {
			return err
		}
		current, err := a.store.GetStorage(r.Context(), item.ID)
		if err != nil {
			return err
		}
		return a.deploy.Storage.DeleteLocked(r.Context(), current)
	})
	if err != nil {
		destructiveOutcome(r, "failed")
		problem(w, 409, "Data deletion stopped", err.Error())
		return
	}
	destructiveOutcome(r, "succeeded")
	w.WriteHeader(204)
}

func (a *API) storageReviewTarget(ctx context.Context, kind, id string) (string, error) {
	switch kind {
	case "application":
		app, err := a.store.GetApp(ctx, id)
		return app.ServerID, err
	case "server":
		return id, nil
	case "service-resource":
		item, err := a.store.(store.ServiceResourceStore).GetServiceResource(ctx, id)
		return item.Target.ServerID, err
	case "service":
		service, err := a.store.GetService(ctx, id)
		if err != nil {
			return "", err
		}
		if service.ProvisionTarget != nil {
			return service.ProvisionTarget.ServerID, nil
		}
	case "storage":
		item, err := a.store.GetStorage(ctx, id)
		return item.ServerID, err
	}
	return "", nil
}

func (a *API) refreshStorageReview(ctx context.Context, kind, id string) error {
	serverID, err := a.storageReviewTarget(ctx, kind, id)
	if err != nil || serverID == "" {
		return err
	}
	server, err := a.store.GetServer(ctx, serverID)
	if err != nil {
		return err
	}
	if server.Runtime != core.ServerRuntimeDocker && !core.IsKubernetesRuntime(server.Runtime) {
		return nil
	}
	if kind == "application" {
		deployed, err := a.store.AppHasDeployments(ctx, id)
		if err != nil {
			return err
		}
		if !deployed {
			return nil
		}
	}
	return a.deploy.Storage.Refresh(ctx, server)
}

func (a *API) addStorageReview(ctx context.Context, out *destructiveReview) (string, error) {
	serverID, err := a.storageReviewTarget(ctx, out.ResourceType, out.ResourceID)
	if err != nil || serverID == "" {
		return "", err
	}
	items, err := a.store.ListStorage(ctx, serverID)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if item.State == "absent" {
			continue
		}
		relevant := out.ResourceType == "server" || out.ResourceType == "storage" && item.ID == out.ResourceID || item.OwnerID == out.ResourceID
		if !relevant {
			continue
		}
		out.Resources = append(out.Resources, fmt.Sprintf("Storage %s: %s, policy %s, %d consumer(s); workload removal retains data", item.Name, item.State, item.Policy, len(item.Consumers)))
		if out.ResourceType == "server" && !item.Independent {
			out.BlockedReason = store.ErrStorageProtected.Error()
		}
	}
	return deploy.StorageVersion(items), nil
}

func (a *API) withStorageRegistrationRemoval(r *http.Request, kind, id string, remove func() error) error {
	serverID, err := a.storageReviewTarget(r.Context(), kind, id)
	if err != nil {
		return err
	}
	if serverID == "" {
		return remove()
	}
	return a.deploy.Storage.WithTarget(r.Context(), serverID, func() error {
		server, err := a.store.GetServer(r.Context(), serverID)
		if err != nil {
			return err
		}
		if server.Runtime == core.ServerRuntimeDocker || core.IsKubernetesRuntime(server.Runtime) {
			if err = a.deploy.Storage.RefreshLocked(r.Context(), server); err != nil {
				return err
			}
		}
		if err = a.recheckDestructiveAction(r, kind, "delete"); err != nil {
			return err
		}
		return remove()
	})
}
