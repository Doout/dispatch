package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

func (a *API) infrastructureQuotaRoutes(r chi.Router) {
	r.With(a.projectPermission(core.PermissionInfrastructureInspect)).Get("/infrastructure/quota", a.getInfrastructureQuota)
	r.With(a.ownerOnly).Put("/infrastructure/quota", a.saveInfrastructureQuota)
}
func (a *API) quotaStore(w http.ResponseWriter) (store.InfrastructureQuotaStore, bool) {
	d, ok := a.store.(store.InfrastructureQuotaStore)
	if !ok {
		problem(w, 503, "Resource policy unavailable", "Durable infrastructure quota storage is required.")
	}
	return d, ok
}
func (a *API) getInfrastructureQuota(w http.ResponseWriter, r *http.Request) {
	d, ok := a.quotaStore(w)
	if !ok {
		return
	}
	project := chi.URLParam(r, "id")
	if _, err := a.store.GetProject(r.Context(), project); err != nil {
		a.notFoundOrInternal(w, err, "Project")
		return
	}
	policy, err := d.GetInfrastructureQuotaPolicy(r.Context(), project)
	if err != nil {
		a.internal(w, err)
		return
	}
	reservations, err := d.ListInfrastructureQuotaReservations(r.Context(), project)
	if err != nil {
		a.internal(w, err)
		return
	}
	usage := map[string]int64{"servers": 0, "reserved": 0, "allocated": 0, "unknown": 0}
	for _, v := range reservations {
		if v.State != "released" {
			usage["servers"]++
			usage[v.State]++
		}
	}
	if snapshots, ok := a.store.(store.InfrastructureSnapshotStore); ok {
		items, e := snapshots.ListInfrastructureSnapshots(r.Context(), project)
		if e != nil {
			a.internal(w, e)
			return
		}
		usage["snapshots"] = 0
		for _, item := range items {
			if item.State != "deleted" && item.State != "cancelled" {
				usage["snapshots"]++
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"policy": policy, "usage": usage, "reservations": reservations})
}
func (a *API) saveInfrastructureQuota(w http.ResponseWriter, r *http.Request) {
	d, ok := a.quotaStore(w)
	if !ok {
		return
	}
	var p core.InfrastructureQuotaPolicy
	if !decode(w, r, &p) {
		return
	}
	p.ProjectID = chi.URLParam(r, "id")
	if _, err := a.store.GetProject(r.Context(), p.ProjectID); err != nil {
		a.notFoundOrInternal(w, err, "Project")
		return
	}
	if p.Revision < 0 || p.MaxServers < -1 || p.MaxTemporaryEnvironments < -1 || p.MaxSnapshots < -1 || p.MaxTemporaryLifetimeSeconds < 0 || p.MaxTemporaryLifetimeSeconds > 365*24*3600 || (p.MaxTemporaryEnvironments != 0 && p.MaxTemporaryLifetimeSeconds == 0) || len(p.Providers) > 100 {
		problem(w, 422, "Invalid resource policy", "Use nonnegative limits, or -1 explicitly for unlimited counts. Temporary environments require a maximum lifetime of up to 365 days.")
		return
	}
	providers, ok := a.store.(store.InfrastructureProviderStore)
	if !ok {
		problem(w, 503, "Provider storage unavailable", "Provider registrations are required.")
		return
	}
	seen := map[string]bool{}
	for _, rule := range p.Providers {
		if rule.ProviderID == "" || seen[rule.ProviderID] {
			problem(w, 422, "Invalid provider rule", "Specify each existing provider at most once.")
			return
		}
		seen[rule.ProviderID] = true
		if _, err := providers.GetInfrastructureProvider(r.Context(), rule.ProviderID); err != nil {
			problem(w, 422, "Invalid provider rule", "Choose an existing provider registration.")
			return
		}
		if rule.AnyRegion && len(rule.Regions) > 0 || rule.AnySize && len(rule.Sizes) > 0 || !validQuotaChoices(rule.Regions) || !validQuotaChoices(rule.Sizes) {
			problem(w, 422, "Invalid allocation choices", "Use explicit region and size lists, or their anyRegion and anySize flags. Empty lists allow no choices.")
			return
		}
	}
	expected := p.Revision
	p.Revision++
	p.Configured = true
	p.UpdatedAt = time.Now().UTC()
	if err := d.SaveInfrastructureQuotaPolicy(r.Context(), p, expected); err != nil {
		if errors.Is(err, store.ErrQuotaPolicyChanged) {
			problem(w, 409, "Resource policy changed", "Reload the current policy before saving.")
			return
		}
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, p)
}
func validQuotaChoices(values []string) bool {
	if len(values) > 500 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range values {
		if v == "" || strings.TrimSpace(v) != v || len(v) > 256 || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}
func infrastructureQuotaProblem(w http.ResponseWriter, err error) bool {
	var violation *core.InfrastructureQuotaViolation
	if !errors.As(err, &violation) {
		return false
	}
	status := http.StatusConflict
	if violation.Code == "allocation_disallowed" {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, map[string]any{"type": "about:blank", "status": status, "title": "Project infrastructure limit", "detail": violation.Error(), "quota": violation})
	return true
}
