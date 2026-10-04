package api

import (
	"net/http"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provision"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

func (a *API) inspectManagedClone(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	item, err := m.InspectClone(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, item)
}
func (a *API) powerManagedServer(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	var in provision.PowerInput
	if !decode(w, r, &in) {
		return
	}
	if in.Action != "start" && in.Action != "stop" && in.Action != "reboot" || in.Revision < 1 {
		problem(w, 422, "Power request invalid", "Choose start, stop or reboot and the current server revision.")
		return
	}
	a.acceptManagedAction(w, r, m, "server."+in.Action, in, false, func(r *http.Request, id, actor, key string) (provision.Accepted, error) {
		return m.Power(r.Context(), id, actor, key, in)
	})
}
func (a *API) promoteManagedClone(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	var in provision.PromotionInput
	if !decode(w, r, &in) {
		return
	}
	if in.Network == "" || in.Revision < 1 || in.ConfirmName == "" {
		problem(w, 422, "Promotion request invalid", "Supply a destination network, current revision and exact clone name.")
		return
	}
	a.acceptManagedAction(w, r, m, "server.promote", in, true, func(r *http.Request, id, actor, key string) (provision.Accepted, error) {
		return m.Promote(r.Context(), id, actor, key, in)
	})
}
func (a *API) acceptManagedAction(w http.ResponseWriter, r *http.Request, m *provision.Manager, action string, input any, promotion bool, submit func(*http.Request, string, string, string) (provision.Accepted, error)) {
	id := chi.URLParam(r, "id")
	data, ok := a.store.(store.InfrastructureLifecycleStore)
	if !ok {
		problem(w, 503, "Infrastructure unavailable", "Durable lifecycle storage is required.")
		return
	}
	server, err := data.GetManagedServer(r.Context(), id)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	if !a.authorizeInfrastructureMutationReceipt(w, r, server.ProjectID, server.ProviderID, core.PermissionInfrastructureModify) {
		return
	}
	if promotion && !a.authorizeInfrastructureMutationReceipt(w, r, server.ProjectID, server.ProviderID, core.PermissionSnapshotRestore) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		problem(w, 422, "Operation identity required", "Supply an Idempotency-Key header and reuse it only for this exact request.")
		return
	}
	r, receipt, proceed := a.reserveMutation(w, r, server.ProjectID, action, input, "infrastructure_operation", id)
	if !proceed {
		return
	}
	item, err := submit(r, id, currentIdentity(r.Context()).Kind+":"+currentIdentity(r.Context()).ID, key)
	if err != nil {
		a.rejectInfrastructureMutation(w, r, err)
		return
	}
	if a.completeInfrastructureMutation(w, r, receipt, item.Operation.ID) {
		return
	}
	writeJSON(w, 202, item)
}
