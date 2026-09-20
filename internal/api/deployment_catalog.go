package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

type deploymentCatalogItem struct {
	AppID        string           `json:"appId"`
	AppName      string           `json:"appName"`
	ProjectID    string           `json:"projectId"`
	TargetID     string           `json:"targetId"`
	TargetName   string           `json:"targetName"`
	Environment  string           `json:"environment"`
	ResourceID   string           `json:"resourceId,omitempty"`
	ResourceName string           `json:"resourceName,omitempty"`
	Latest       *core.Deployment `json:"latest,omitempty"`
	Current      *core.Deployment `json:"current,omitempty"`
	Sync         *catalogSync     `json:"sync,omitempty"`
}
type catalogSync struct {
	StaleAfterSeconds int        `json:"staleAfterSeconds"`
	Configuration     string     `json:"configuration"`
	Revision          string     `json:"revision"`
	Drift             string     `json:"drift"`
	Health            string     `json:"health"`
	Supported         bool       `json:"supported"`
	CheckedAt         *time.Time `json:"checkedAt,omitempty"`
	Message           string     `json:"message"`
	HealthMessage     string     `json:"healthMessage,omitempty"`
}

func (a *API) catalogItems(r *http.Request) ([]deploymentCatalogItem, error) {
	ctx := r.Context()
	visible, err := a.visibleProjectIDs(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := a.store.ListActiveApps(ctx)
	if err != nil {
		return nil, err
	}
	servers, err := a.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	targets := map[string]string{}
	for _, s := range servers {
		targets[s.ID] = s.Name
	}
	runs, err := a.store.ListWorkflowStageRuns(ctx, "")
	if err != nil {
		return nil, err
	}
	// The last matching retained run is authoritative; a generated application's
	// environment cannot be inferred safely from its name.
	runByDeployment := map[string]core.WorkflowStageRun{}
	for _, run := range runs {
		for _, id := range run.DeploymentIDs {
			runByDeployment[id] = run
		}
	}
	items := []deploymentCatalogItem{}
	for _, app := range apps {
		if !visible[app.ProjectID] || app.Template {
			continue
		}
		item := deploymentCatalogItem{AppID: app.ID, AppName: app.Name, ProjectID: app.ProjectID, TargetID: app.ServerID, TargetName: targets[app.ServerID], Environment: targets[app.ServerID]}
		history, e := a.store.ListApplicationHistory(ctx, app.ID, "", 1)
		if e != nil {
			return nil, e
		}
		if len(history) > 0 {
			item.Latest = &history[0]
		}
		current, e := a.store.LatestSuccessfulDeployment(ctx, app.ID)
		if e != nil && !errors.Is(e, store.ErrNotFound) {
			return nil, e
		}
		if e == nil {
			current.App = nil
			current.Server = nil
			current.Outputs = nil
			current.Message = ""
			item.Current = &current
		}
		var run core.WorkflowStageRun
		if item.Latest != nil {
			run = runByDeployment[item.Latest.ID]
		}
		if run.ID == "" && item.Current != nil {
			run = runByDeployment[item.Current.ID]
		}
		if run.ID != "" {
			revision, e := a.store.GetWorkflowRevision(ctx, run.RevisionID)
			if e != nil {
				return nil, e
			}
			resource, e := a.store.GetWorkflowResource(ctx, revision.ResourceID)
			if e != nil {
				return nil, e
			}
			source, e := a.store.GetConfigSource(ctx, resource.ConfigSourceID)
			if e != nil {
				return nil, e
			}
			if source.ProjectID == app.ProjectID {
				item.Environment = run.StageName
				item.ResourceID = resource.ID
				item.ResourceName = resource.Name
			}
		}
		status, e := a.applicationSync(ctx, app.ID)
		if e != nil {
			return nil, e
		}
		item.Sync = &catalogSync{Configuration: status.Configuration.State, Revision: status.Revision.State, Drift: status.Drift.State, Health: status.Drift.Health, Supported: status.Supported, CheckedAt: status.Drift.CheckedAt, Message: status.Drift.Message, HealthMessage: status.Drift.HealthMessage}
		item.Sync.StaleAfterSeconds = 900
		if a.observations != nil {
			configuration, e := a.observations.Config(ctx, app.ID)
			if e != nil {
				return nil, e
			}
			item.Sync.StaleAfterSeconds = configuration.StaleAfterSeconds
		}

		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AppName < items[j].AppName })
	return items, nil
}
func (a *API) deploymentCatalog(w http.ResponseWriter, r *http.Request) {
	items, err := a.catalogItems(r)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Items []deploymentCatalogItem `json:"items"`
	}{items})
}

func (a *API) deploymentSearch(w http.ResponseWriter, r *http.Request) {
	items, err := a.catalogItems(r)
	if err != nil {
		a.internal(w, err)
		return
	}
	q := r.URL.Query()
	ids := []string{}
	for _, item := range items {
		if q.Get("project") != "" && q.Get("project") != item.ProjectID {
			continue
		}
		if q.Get("application") != "" && q.Get("application") != item.AppID && q.Get("application") != item.ResourceID {
			continue
		}
		if q.Get("environment") != "" && q.Get("environment") != item.Environment {
			continue
		}
		if q.Get("target") != "" && q.Get("target") != item.TargetID {
			continue
		}
		ids = append(ids, item.AppID)
	}
	before := q.Get("before")
	if before != "" {
		cursor, e := a.store.GetDeployment(r.Context(), before)
		allowed := false
		for _, id := range ids {
			if cursor.AppID == id {
				allowed = true
			}
		}
		if errors.Is(e, store.ErrNotFound) || e == nil && !allowed {
			problem(w, 404, "Deployment not found", "The history cursor is outside this selection.")
			return
		}
		if e != nil {
			a.internal(w, e)
			return
		}
	}
	if len(q.Get("q")) > 200 || len(q.Get("revision")) > 200 {
		problem(w, 400, "Search too long", "Use no more than 200 characters.")
		return
	}
	results, err := a.store.SearchDeploymentHistory(r.Context(), core.DeploymentSearch{AppIDs: ids, Before: before, Query: strings.TrimSpace(q.Get("q")), State: q.Get("status"), Revision: q.Get("revision"), Limit: 51})
	if err != nil {
		a.internal(w, err)
		return
	}
	next := ""
	if len(results) > 50 {
		results = results[:50]
		next = results[49].ID
	}
	writeJSON(w, 200, struct {
		Items []core.Deployment `json:"items"`
		Next  string            `json:"next,omitempty"`
	}{results, next})
}

// Both applications must be visible and belong to one project. This endpoint
// intentionally allows different applications for environments whose generated
// runtime application IDs differ. It uses only immutable, redacted snapshots.
func (a *API) compareEnvironments(w http.ResponseWriter, r *http.Request) {
	to, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	from, err := a.store.GetDeployment(r.Context(), r.URL.Query().Get("from"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, 404, "Deployment not found", "Select an accessible deployment.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	if from.App == nil || to.App == nil || from.App.ProjectID != to.App.ProjectID {
		problem(w, 404, "Deployment not found", "Compare environments within one project.")
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectView, from.App.ProjectID) {
		return
	}
	writeJSON(w, 200, compareDeploymentSnapshots(from, to))
}

func (a *API) deploymentIdentity(w http.ResponseWriter, r *http.Request) {
	d, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	item := deploymentCatalogItem{AppID: d.AppID}
	if d.App != nil {
		item.AppName = d.App.Name
		item.ProjectID = d.App.ProjectID
	}
	// Never substitute today's target for a missing historical snapshot.
	item.TargetID = d.Snapshot.TargetID
	item.TargetName = d.Snapshot.TargetName
	if item.TargetID != "" && item.TargetName == "" {
		target, e := a.store.GetServer(r.Context(), item.TargetID)
		if e == nil {
			item.TargetName = target.Name
		}
	}
	latest, err := a.store.ListApplicationHistory(r.Context(), d.AppID, "", 1)
	if err != nil {
		a.internal(w, err)
		return
	}
	if len(latest) > 0 {
		item.Latest = &latest[0]
	}
	current, err := a.store.LatestSuccessfulDeployment(r.Context(), d.AppID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.internal(w, err)
		return
	}
	if err == nil {
		current.App = nil
		current.Server = nil
		current.Outputs = nil
		current.Message = ""
		item.Current = &current
	}
	runs, err := a.store.ListWorkflowStageRuns(r.Context(), "")
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, run := range runs {
		for _, id := range run.DeploymentIDs {
			if id == d.ID {
				item.Environment = run.StageName
			}
		}
	}
	if item.Environment == "" {
		item.Environment = d.Snapshot.Namespace
	}
	writeJSON(w, 200, item)
}
