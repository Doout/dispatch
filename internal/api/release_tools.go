package api

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

func (a *API) registerReleaseRoutes(r chi.Router) {
	r.Post("/apps/{id}/release-preview", a.appPermission(core.PermissionDeploymentRun, a.previewApplicationRelease))
	r.Get("/apps/{id}/activity", a.appPermission(core.PermissionProjectView, a.applicationReleaseActivity))
	r.Get("/deployments/{id}/release", a.deploymentPermission(core.PermissionProjectView, a.getDeploymentRelease))
	r.Put("/deployments/{id}/release", a.deploymentPermission(core.PermissionProjectConfigure, a.updateDeploymentRelease))
	r.Post("/deployments/{id}/rollback-preview", a.deploymentPermission(core.PermissionDeploymentRun, a.previewDeploymentRollback))
	r.Post("/deployments/{id}/rollback", a.deploymentPermission(core.PermissionDeploymentRun, a.rollbackDeploymentRelease))
	r.Get("/deployments/{id}/diagnosis", a.deploymentPermission(core.PermissionProjectView, a.diagnoseDeploymentRelease))
}

func (a *API) previewApplicationRelease(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision string `json:"revision"`
	}
	if r.ContentLength > 0 && !decode(w, r, &input) {
		return
	}
	if len(input.Revision) > 200 || strings.HasPrefix(input.Revision, "-") || strings.ContainsAny(input.Revision, "\x00\n\r") {
		problem(w, 422, "Invalid revision", "Choose a source commit or branch.")
		return
	}
	app, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	server, err := a.store.GetServer(r.Context(), app.ServerID)
	if err != nil {
		a.internal(w, err)
		return
	}
	var result deploy.ReleasePreview
	err = a.deploy.WithIdleApplication(r.Context(), app.ID, func() error {
		sourceAuth := deploy.SourceAuthExecutor{Secrets: a.store, Vault: a.eventConfig.Vault}
		if a.eventConfig.SecretResolver != nil {
			sourceAuth.Resolver = a.eventConfig.SecretResolver
		}
		if a.eventConfig.GitHubApps != nil {
			sourceAuth.GitHubApps = a.eventConfig.GitHubApps
		}
		result = a.deploy.PreviewRelease(r.Context(), app, server, input.Revision, sourceAuth)
		return nil
	})
	if err != nil {
		problem(w, 409, "Preview unavailable", "Wait for the active deployment or operation to finish.")
		return
	}
	comparison := deploymentComparison{Changes: []deploymentChange{}, Message: "No successful deployment exists to compare."}
	if current, err := a.store.LatestSuccessfulDeployment(r.Context(), app.ID); err == nil {
		comparison = compareDeploymentSnapshots(current, core.Deployment{ID: "preview", CommitSHA: result.Revision, Snapshot: result.Snapshot})
	}
	writeJSON(w, 200, struct {
		deploy.ReleasePreview
		Target     string                       `json:"target"`
		Namespace  string                       `json:"namespace"`
		Release    string                       `json:"release"`
		Bindings   []core.AppliedServiceBinding `json:"bindings"`
		Comparison deploymentComparison         `json:"comparison"`
		Message    string                       `json:"message"`
	}{result, server.Name, result.Snapshot.Namespace, result.Snapshot.Release, result.Snapshot.ServiceBindings, comparison, "This is a point-in-time preview. Target state and external credentials can change before deployment."})
}

type deploymentReleaseResponse struct {
	Note            core.ReleaseNote `json:"note"`
	SourceLinks     []string         `json:"sourceLinks"`
	RollbackMessage string           `json:"rollbackMessage"`
}

func (a *API) getDeploymentRelease(w http.ResponseWriter, r *http.Request) {
	d, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	data, ok := a.store.(store.ReleaseStore)
	if !ok {
		problem(w, 503, "Release history unavailable", "Release storage is not configured.")
		return
	}
	note, err := data.GetReleaseNote(r.Context(), d.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	links := []string{}
	if d.App != nil {
		if link := commitSourceLink(d.App.SourceRepo, d.CommitSHA); link != "" {
			links = append(links, link)
		}
	}
	message := "Select a successful retained Helm version and review its inputs before rollback. Database migrations and external effects are not reversed."
	if d.State != core.DeploymentSucceeded {
		message = "Only successful deployments can be selected as a rollback version."
	}
	writeJSON(w, 200, deploymentReleaseResponse{note, links, message})
}

var sourceCommit = regexp.MustCompile(`^[a-fA-F0-9]{7,64}$`)

func commitSourceLink(repository, revision string) string {
	if !sourceCommit.MatchString(revision) {
		return ""
	}
	repository = strings.TrimSpace(repository)
	if strings.HasPrefix(repository, "git@") {
		parts := strings.SplitN(strings.TrimPrefix(repository, "git@"), ":", 2)
		if len(parts) != 2 {
			return ""
		}
		repository = "https://" + parts[0] + "/" + parts[1]
	}
	u, err := url.Parse(repository)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "ssh") {
		return ""
	}
	u.Scheme = "https"
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git") + "/commit/" + revision
	return u.String()
}

func (a *API) updateDeploymentRelease(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Notes string   `json:"notes"`
		Links []string `json:"links"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.Notes) > 12000 || len(input.Links) > 10 {
		problem(w, 422, "Release notes too large", "Use up to 12000 characters and 10 source links.")
		return
	}
	for _, link := range input.Links {
		u, err := url.Parse(link)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || len(link) > 2000 || strings.ContainsAny(link, "\r\n\x00") {
			problem(w, 422, "Invalid source link", "Use HTTPS links without embedded credentials.")
			return
		}
	}
	d, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	data, ok := a.store.(store.ReleaseStore)
	if !ok {
		problem(w, 503, "Release history unavailable", "Release storage is not configured.")
		return
	}
	now := time.Now().UTC()
	actor := currentIdentity(r.Context()).ID
	note := core.ReleaseNote{DeploymentID: d.ID, Notes: strings.TrimSpace(input.Notes), Links: input.Links, Actor: actor, UpdatedAt: now}
	if note.Links == nil {
		note.Links = []string{}
	}
	action := core.ReleaseAction{ID: ulid.Make().String(), ProjectID: d.App.ProjectID, AppID: d.AppID, DeploymentID: d.ID, Actor: actor, Action: "release.notes", Message: "Release notes and source links updated", CreatedAt: now}
	if err = data.SaveReleaseNote(r.Context(), note, action); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, note)
}

func (a *API) previewDeploymentRollback(w http.ResponseWriter, r *http.Request) {
	preview, err := a.deploy.PreviewRollback(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		problem(w, 409, "Rollback unavailable", err.Error())
		return
	}
	writeJSON(w, 200, preview)
}
func (a *API) rollbackDeploymentRelease(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ConfirmDeploymentID         string `json:"confirmDeploymentId"`
		ExpectedCurrentDeploymentID string `json:"expectedCurrentDeploymentId"`
		ConfirmDatabaseNotReverted  bool   `json:"confirmDatabaseNotReverted"`
	}
	if !decode(w, r, &input) {
		return
	}
	id := chi.URLParam(r, "id")
	if input.ConfirmDeploymentID != id || input.ExpectedCurrentDeploymentID == "" || !input.ConfirmDatabaseNotReverted {
		problem(w, 422, "Review rollback first", "Confirm the selected deployment, current release, and that database migrations are not reverted.")
		return
	}
	var capture deploy.ReleaseCapture
	if a.drift != nil {
		capture = a.drift.Capture
	}
	d, err := a.deploy.StartRollback(r.Context(), id, input.ExpectedCurrentDeploymentID, currentIdentity(r.Context()).ID, capture)
	if err != nil {
		problem(w, 409, "Rollback stopped", err.Error())
		return
	}
	if currentIdentity(r.Context()).SystemRole != core.UserRoleOwner && d.App != nil {
		app := redactAppCredentials(*d.App)
		d.App = &app
	}
	writeJSON(w, 202, d)
}

type releaseActivity struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	State        string    `json:"state,omitempty"`
	Message      string    `json:"message"`
	Actor        string    `json:"actor,omitempty"`
	DeploymentID string    `json:"deploymentId,omitempty"`
	Revision     string    `json:"revision,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (a *API) applicationReleaseActivity(w http.ResponseWriter, r *http.Request) {
	app, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	items := []releaseActivity{}
	history, err := a.store.ListApplicationHistory(r.Context(), app.ID, "", 100)
	if err != nil {
		a.internal(w, err)
		return
	}
	deploymentIDs := map[string]bool{}
	for _, d := range history {
		deploymentIDs[d.ID] = true
		items = append(items, releaseActivity{ID: d.ID, Kind: "deployment", State: string(d.State), Message: "Deployment " + string(d.State), DeploymentID: d.ID, Revision: d.CommitSHA, CreatedAt: d.CreatedAt})
	}
	if check, err := a.store.GetDriftCheck(r.Context(), app.ID); err == nil && check.CheckedAt != nil {
		items = append(items, releaseActivity{ID: "observation:" + check.CheckedAt.String(), Kind: "observation", State: check.State, Message: "Runtime drift: " + check.State + "; workload health: " + check.Health, DeploymentID: check.DeploymentID, CreatedAt: *check.CheckedAt})
	}
	if actions, err := a.store.ListDriftActions(r.Context(), app.ID); err == nil {
		for _, action := range actions {
			items = append(items, releaseActivity{ID: action.ID, Kind: "reapply", State: action.State, Message: action.Message, Actor: action.Actor, DeploymentID: action.DeploymentID, CreatedAt: action.CreatedAt})
		}
	}
	if data, ok := a.store.(store.ReleaseStore); ok {
		actions, err := data.ListReleaseActions(r.Context(), app.ProjectID, app.ID)
		if err != nil {
			a.internal(w, err)
			return
		}
		for _, action := range actions {
			items = append(items, releaseActivity{ID: action.ID, Kind: action.Action, Message: action.Message, Actor: action.Actor, DeploymentID: action.DeploymentID, CreatedAt: action.CreatedAt})
		}
	}

	if audit, ok := a.store.(interface {
		ListAuditEvents(context.Context, core.AuditFilter) ([]core.AuditEvent, error)
	}); ok {
		rows, err := audit.ListAuditEvents(r.Context(), core.AuditFilter{ProjectIDs: []string{app.ProjectID}, AppID: app.ID, Limit: 100})
		if err != nil {
			a.internal(w, err)
			return
		}
		for _, event := range rows {
			actor := event.ActorName
			if actor == "" {
				actor = event.ActorID
			}
			items = append(items, releaseActivity{ID: event.ID, Kind: "audit", State: event.Outcome, Message: event.Action, Actor: actor, CreatedAt: event.CreatedAt})
		}
	}
	// Match stages by their retained deployment IDs, never by user-visible names.
	revisions, err := a.store.ListWorkflowRevisions(r.Context(), "", 100)
	if err == nil {
		visibleResources := map[string]bool{}
		checkedResources := map[string]bool{}
		for _, revision := range revisions {
			if !checkedResources[revision.ResourceID] {
				checkedResources[revision.ResourceID] = true
				if resource, e := a.store.GetWorkflowResource(r.Context(), revision.ResourceID); e == nil {
					if source, e := a.store.GetConfigSource(r.Context(), resource.ConfigSourceID); e == nil {
						visibleResources[revision.ResourceID] = source.ProjectID == app.ProjectID
					}
				}
			}
			if !visibleResources[revision.ResourceID] {
				continue
			}
			stages, e := a.store.ListWorkflowStageRuns(r.Context(), revision.ID)
			if e != nil {
				continue
			}
			matched := false
			for _, stage := range stages {
				for _, id := range stage.DeploymentIDs {
					if deploymentIDs[id] {
						matched = true
						items = append(items, releaseActivity{ID: stage.ID, Kind: "stage", State: stage.State, Message: stage.StageName + ": " + stage.State + "; approval " + stage.Approval, DeploymentID: id, Revision: revision.ID, CreatedAt: stage.CreatedAt})
						break
					}
				}
			}
			if matched {
				items = append(items, releaseActivity{ID: revision.ID, Kind: "source", State: revision.State, Message: "Workflow revision accepted: " + revision.Trigger, Revision: revision.ConfigSHA, CreatedAt: revision.CreatedAt})
				jobs, e := a.store.ListWorkflowJobResults(r.Context(), revision.ID)
				if e == nil {
					for _, job := range jobs {
						items = append(items, releaseActivity{ID: job.ID, Kind: "build", State: job.State, Message: job.JobName + ": " + job.State, Revision: revision.ID, CreatedAt: job.CreatedAt})
					}
				}
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if len(items) > 200 {
		items = items[:200]
	}
	writeJSON(w, 200, struct {
		Items   []releaseActivity `json:"items"`
		Message string            `json:"message"`
	}{items, "Recent retained deployment, build, stage, observation, and operator events."})
}
