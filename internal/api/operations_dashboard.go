package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

// nil scope is reserved for controller owners and includes controller-wide audit
// events; an empty non-nil scope always produces an empty project result.
func (a *API) operationsScope(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	project := strings.TrimSpace(r.URL.Query().Get("projectId"))
	if project != "" {
		if !a.requireProject(w, r, core.PermissionProjectView, project) {
			return nil, false
		}
		return []string{project}, true
	}
	if currentIdentity(r.Context()).SystemRole == core.UserRoleOwner {
		return nil, true
	}
	visible, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return nil, false
	}
	result := []string{}
	for id := range visible {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, true
}
func (a *API) operationsAuditFilter(w http.ResponseWriter, r *http.Request) (core.AuditFilter, bool) {
	q := r.URL.Query()
	f := core.AuditFilter{AppID: q.Get("appId"), ActorID: q.Get("actorId"), Action: q.Get("action"), Before: q.Get("before"), Query: strings.TrimSpace(q.Get("q")), Outcome: q.Get("outcome"), Limit: 100}
	if len(f.Query) > 200 {
		problem(w, 400, "Search too long", "Use no more than 200 characters.")
		return f, false
	}
	if f.Outcome != "" && f.Outcome != "succeeded" && f.Outcome != "rejected" {
		problem(w, 400, "Invalid outcome", "Choose succeeded or rejected.")
		return f, false
	}
	for _, bound := range []struct {
		name   string
		target **time.Time
	}{{"since", &f.Since}, {"until", &f.Until}} {
		if q.Has(bound.name) {
			parsed, err := time.Parse(time.RFC3339Nano, q.Get(bound.name))
			parsed = parsed.UTC()
			if err != nil || parsed.Year() < 1 || parsed.Year() > 9999 {
				problem(w, 400, "Invalid audit range", "Use RFC3339 timestamps for since and until.")
				return f, false
			}
			*bound.target = &parsed
		}
	}
	if f.Since != nil && f.Until != nil && !f.Since.Before(*f.Until) {
		problem(w, 400, "Invalid audit range", "The audit range end must be after its start.")
		return f, false
	}
	var ok bool
	f.ProjectIDs, ok = a.operationsScope(w, r)
	return f, ok
}
func (a *API) operationsSummary(w http.ResponseWriter, r *http.Request) {
	projects, ok := a.operationsScope(w, r)
	if !ok {
		return
	}
	data, ok := a.ops(w)
	if !ok {
		return
	}
	owner := currentIdentity(r.Context()).SystemRole == core.UserRoleOwner
	summary, err := data.GetOperationsSummary(r.Context(), projects, owner, time.Now().UTC())
	if err != nil {
		a.internal(w, err)
		return
	}
	if summary.Backups != nil {
		summary.Backups.Configured = a.eventConfig.BackupDirectory != "" && a.eventConfig.MasterKeyFile != ""
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, summary)
}

type ownershipCursor struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

func (a *API) operationsOwnership(w http.ResponseWriter, r *http.Request) {
	projects, ok := a.operationsScope(w, r)
	if !ok {
		return
	}
	data, ok := a.ops(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := core.OwnershipFilter{ProjectIDs: projects, Query: strings.TrimSpace(q.Get("q")), Limit: 101}
	if len(f.Query) > 200 {
		problem(w, 400, "Search too long", "Use no more than 200 characters.")
		return
	}
	if q.Has("unassigned") {
		if q.Get("unassigned") != "true" && q.Get("unassigned") != "false" {
			problem(w, 400, "Invalid ownership filter", "Use true or false for unassigned.")
			return
		}
		f.Unassigned = q.Get("unassigned") == "true"
		f.Assigned = !f.Unassigned
	}
	if raw := q.Get("before"); raw != "" {
		if len(raw) > 4096 {
			problem(w, 400, "Invalid ownership cursor", "Refresh the ownership list and try again.")
			return
		}
		var cursor ownershipCursor
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.ID == "" {
			problem(w, 400, "Invalid ownership cursor", "Refresh the ownership list and try again.")
			return
		}
		f.BeforeName, f.BeforeID = cursor.Name, cursor.ID
	}
	items, err := data.ListOperationsOwnership(r.Context(), f)
	if err != nil {
		a.internal(w, err)
		return
	}
	next := ""
	if len(items) > 100 {
		items = items[:100]
		last := items[len(items)-1]
		payload, _ := json.Marshal(ownershipCursor{Name: last.AppName, ID: last.AppID})
		next = base64.RawURLEncoding.EncodeToString(payload)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, struct {
		Items []core.OwnershipItem `json:"items"`
		Next  string               `json:"next,omitempty"`
	}{items, next})
}
