package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

func (a *API) applicationDeploymentHistory(w http.ResponseWriter, r *http.Request) {
	appID, before := chi.URLParam(r, "id"), r.URL.Query().Get("before")
	if before != "" {
		item, err := a.store.GetDeployment(r.Context(), before)
		if errors.Is(err, store.ErrNotFound) || err == nil && item.AppID != appID {
			problem(w, 404, "Deployment not found", "The history cursor does not belong to this application.")
			return
		}
		if err != nil {
			a.internal(w, err)
			return
		}
	}
	items, err := a.store.ListApplicationHistory(r.Context(), appID, before, 51)
	if err != nil {
		a.internal(w, err)
		return
	}
	next := ""
	if len(items) > 50 {
		items = items[:50]
		next = items[49].ID
	}
	writeJSON(w, 200, struct {
		Items []core.Deployment `json:"items"`
		Next  string            `json:"next,omitempty"`
	}{items, next})
}

type deploymentChange struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}
type deploymentComparison struct {
	FromID    string             `json:"fromId"`
	ToID      string             `json:"toId"`
	Available bool               `json:"available"`
	Changes   []deploymentChange `json:"changes"`
	Hidden    int                `json:"hidden"`
	Truncated bool               `json:"truncated"`
	Message   string             `json:"message"`
}

func (a *API) compareDeployments(w http.ResponseWriter, r *http.Request) {
	to, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	from, err := a.store.GetDeployment(r.Context(), r.URL.Query().Get("from"))
	// Compare only versions of this authorized application, including for owners.
	if errors.Is(err, store.ErrNotFound) || err == nil && from.AppID != to.AppID {
		problem(w, 404, "Deployment not found", "Select another deployment of this application.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, compareDeploymentSnapshots(from, to))
}

func compareDeploymentSnapshots(from, to core.Deployment) deploymentComparison {
	result := deploymentComparison{FromID: from.ID, ToID: to.ID, Changes: []deploymentChange{}, Message: "Saved deployment inputs. Sensitive values are excluded; chart defaults and live changes are not compared."}
	if from.Snapshot.TargetID == "" || to.Snapshot.TargetID == "" {
		result.Message = "Saved inputs are unavailable for one of these deployments. Current application settings are not used as historical values."
		return result
	}
	result.Available = true
	left, right := historicalFields(from), historicalFields(to)
	keys := map[string]bool{}
	for key := range left {
		keys[key] = true
	}
	for key := range right {
		keys[key] = true
	}
	paths := make([]string, 0, len(keys))
	for key := range keys {
		paths = append(paths, key)
	}
	sort.Strings(paths)
	for _, path := range paths {
		before, bok := left[path]
		after, aok := right[path]
		if before == hiddenHistoryValue || after == hiddenHistoryValue {
			result.Hidden++
			continue
		}
		if bok == aok && reflect.DeepEqual(before, after) {
			continue
		}
		if len(result.Changes) == 1000 {
			result.Truncated = true
			continue
		}
		kind := "changed"
		if !bok {
			kind = "added"
		}
		if !aok {
			kind = "removed"
		}
		result.Changes = append(result.Changes, deploymentChange{path, kind, before, after})
	}
	return result
}

const hiddenHistoryValue = "••••••••"

func historicalFields(d core.Deployment) map[string]any {
	s := d.Snapshot
	fields := map[string]any{"/revision": d.CommitSHA, "/release/target": s.TargetID, "/release/namespace": s.Namespace, "/release/name": s.Release, "/release/chart": s.Chart, "/release/runtime": s.Runtime}
	flattenHistorical("/values", s.Values, fields)
	for _, binding := range s.ServiceBindings {
		base := "/services/" + pointerPart(binding.Alias)
		fields[base+"/service"] = binding.ServiceName
		fields[base+"/revision"] = binding.Revision
	}
	return fields
}
func pointerPart(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
func flattenHistorical(path string, value any, fields map[string]any) {
	// Some chart values contain environment entries, URLs, or entire YAML documents.
	// Never expose those in a comparison even when a chart uses an ordinary key.
	lower := strings.ToLower(path)
	if sensitivePath(path) || strings.Contains(lower, "connection") || hiddenHistoryPath(lower) {
		fields[path] = hiddenHistoryValue
		return
	}
	switch v := value.(type) {
	case map[string]any:
		if len(v) == 0 {
			fields[path] = map[string]any{}
		}
		for key, item := range v {
			flattenHistorical(path+"/"+pointerPart(key), item, fields)
		}
	case []any:
		if len(v) == 0 {
			fields[path] = []any{}
		}
		for i, item := range v {
			flattenHistorical(fmt.Sprintf("%s/%d", path, i), item, fields)
		}
	case string:
		if strings.ContainsAny(v, "\n\r") || strings.Contains(v, "://") && (strings.Contains(v, "@") || strings.Contains(v, "?")) || strings.Contains(v, "PRIVATE KEY") || strings.Contains(v, hiddenHistoryValue) {
			fields[path] = hiddenHistoryValue
		} else {
			fields[path] = v
		}
	default:
		// Normalize numbers from hand-built snapshots and persisted JSON alike.
		raw, _ := json.Marshal(v)
		var normalized any
		_ = json.Unmarshal(raw, &normalized)
		fields[path] = normalized
	}
}

func hiddenHistoryPath(path string) bool {
	for _, part := range strings.Split(path, "/") {
		switch part {
		case "env", "envfrom", "extraenv", "extraenvvars", "environment", "data", "stringdata", "authorization", "auth", "dsn":
			return true
		}
	}
	return false
}
