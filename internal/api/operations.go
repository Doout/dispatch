package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/oklog/ulid/v2"
)

type operationsStore interface {
	AppendAuditEvent(context.Context, core.AuditEvent) error
	ListAuditEvents(context.Context, core.AuditFilter) ([]core.AuditEvent, error)
	GetApplicationOwner(context.Context, string) (core.ApplicationOwner, error)
	SaveApplicationOwner(context.Context, core.ApplicationOwner) error
	ListIdentityTeamMappings(context.Context) ([]core.IdentityTeamMapping, error)
	SaveIdentityTeamMapping(context.Context, core.IdentityTeamMapping) error
	DeleteIdentityTeamMapping(context.Context, string) error
	GetRetentionPolicy(context.Context, string) (core.RetentionPolicy, error)
	SaveRetentionPolicy(context.Context, core.RetentionPolicy) error
	ApplyRetention(context.Context, core.RetentionPolicy, bool, time.Time) (core.RetentionResult, error)
	SaveBackupRecord(context.Context, core.BackupRecord) error
	ListBackupRecords(context.Context) ([]core.BackupRecord, error)
}

func (a *API) operationsRoutes(r chi.Router) {
	r.Get("/audit", a.listAudit)
	r.Get("/projects/{id}/owner-candidates", a.ownerCandidates)
	r.Get("/apps/{id}/owner", a.appPermission(core.PermissionProjectView, a.getApplicationOwner))
	r.Put("/apps/{id}/owner", a.appPermission(core.PermissionProjectConfigure, a.setApplicationOwner))
	r.Get("/identity-team-mappings", a.ownerOnly(a.listTeamMappings))
	r.Post("/identity-team-mappings", a.ownerOnly(a.saveTeamMapping))
	r.Delete("/identity-team-mappings/{id}", a.ownerOnly(a.removeTeamMapping))
	r.Get("/projects/{id}/retention", a.getRetention)
	r.Put("/projects/{id}/retention", a.saveRetention)
	r.Post("/projects/{id}/retention/preview", a.previewRetention)
	r.Post("/projects/{id}/retention/apply", a.applyRetention)
	r.Get("/operations/backups", a.ownerOnly(a.listBackups))
	r.Post("/operations/backups", a.ownerOnly(a.createBackup))
	r.Post("/operations/backups/{id}/verify", a.ownerOnly(a.verifyBackup))
	r.Get("/services/{id}/impact", a.serviceImpact)
	r.Post("/services/{id}/redeploy", a.redeployServiceConsumers)
}
func (a *API) ops(w http.ResponseWriter) (operationsStore, bool) {
	s, ok := a.store.(operationsStore)
	if !ok {
		problem(w, 503, "Operations unavailable", "The configured store does not support operations.")
	}
	return s, ok
}
func (a *API) auditMutation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" {
			next.ServeHTTP(w, r)
			return
		}
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		data, ok := a.store.(operationsStore)
		if !ok {
			return
		}
		identity := currentIdentity(r.Context())
		if identity.ID == "" {
			return
		}
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			return
		}
		id := chi.URLParam(r, "id")
		e := core.AuditEvent{ID: ulid.Make().String(), ActorID: identity.ID, ActorName: identity.DisplayName, Action: r.Method + " " + route, ResourceID: id, Outcome: "succeeded", CreatedAt: time.Now().UTC()}
		if ww.Status() >= 400 {
			e.Outcome = "rejected"
		}
		if actor, ok := currentImpersonator(r.Context()); ok {
			e.ImpersonatorID = actor.ID
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		switch {
		case strings.Contains(route, "/apps/{id}"):
			if app, err := a.store.GetApp(ctx, id); err == nil {
				e.ProjectID, e.AppID = app.ProjectID, app.ID
			}
		case strings.Contains(route, "/deployments/{id}"):
			if d, err := a.store.GetDeployment(ctx, id); err == nil {
				if app, err := a.store.GetApp(ctx, d.AppID); err == nil {
					e.ProjectID, e.AppID = app.ProjectID, app.ID
				}
			}
		case strings.Contains(route, "/services/{id}"):
			if v, err := a.store.GetService(ctx, id); err == nil {
				e.ProjectID = v.ProjectID
			}
		case strings.Contains(route, "/projects/{id}"):
			e.ProjectID = id
		case strings.Contains(route, "/workflow/stages/{id}"):
			if stage, err := a.store.GetWorkflowStageRun(ctx, id); err == nil {
				if revision, err := a.store.GetWorkflowRevision(ctx, stage.RevisionID); err == nil {
					if resource, err := a.store.GetWorkflowResource(ctx, revision.ResourceID); err == nil {
						if source, err := a.store.GetConfigSource(ctx, resource.ConfigSourceID); err == nil {
							e.ProjectID = source.ProjectID
						}
					}
				}
			}
		}
		if err := data.AppendAuditEvent(ctx, e); err != nil {
			a.logger.Error("audit event could not be saved", "action", e.Action)
		}
	})
}
func (a *API) listAudit(w http.ResponseWriter, r *http.Request) {
	data, ok := a.ops(w)
	if !ok {
		return
	}
	f := core.AuditFilter{AppID: r.URL.Query().Get("appId"), ActorID: r.URL.Query().Get("actorId"), Action: r.URL.Query().Get("action"), Before: r.URL.Query().Get("before"), Limit: 100}
	project := r.URL.Query().Get("projectId")
	if project != "" {
		if !a.requireProject(w, r, core.PermissionProjectView, project) {
			return
		}
		f.ProjectIDs = []string{project}
	} else if currentIdentity(r.Context()).SystemRole != core.UserRoleOwner {
		visible, err := a.visibleProjectIDs(r.Context())
		if err != nil {
			a.internal(w, err)
			return
		}
		f.ProjectIDs = []string{}
		for id := range visible {
			f.ProjectIDs = append(f.ProjectIDs, id)
		}
	}
	rows, err := data.ListAuditEvents(r.Context(), f)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, rows)
}
func (a *API) getApplicationOwner(w http.ResponseWriter, r *http.Request) {
	data, ok := a.ops(w)
	if !ok {
		return
	}
	o, err := data.GetApplicationOwner(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	name := ""
	if o.PrincipalID != "" {
		if o.PrincipalType == core.PrincipalUser {
			if person, e := a.store.GetUser(r.Context(), o.PrincipalID); e == nil {
				name = person.DisplayName
				if name == "" {
					name = person.Username
				}
			}
		}
		if o.PrincipalType == core.PrincipalTeam {
			if team, e := a.store.GetTeam(r.Context(), o.PrincipalID); e == nil {
				name = team.Name
			}
		}
	}
	writeJSON(w, 200, struct {
		core.ApplicationOwner
		DisplayName string `json:"displayName,omitempty"`
	}{o, name})
}
func (a *API) setApplicationOwner(w http.ResponseWriter, r *http.Request) {
	data, ok := a.ops(w)
	if !ok {
		return
	}
	var o core.ApplicationOwner
	if !decode(w, r, &o) {
		return
	}
	o.AppID = chi.URLParam(r, "id")
	o.UpdatedAt = time.Now().UTC()
	if o.PrincipalID != "" {
		app, err := a.store.GetApp(r.Context(), o.AppID)
		if err != nil {
			a.internal(w, err)
			return
		}
		eligible := false
		switch o.PrincipalType {
		case core.PrincipalUser:
			user, e := a.store.GetUser(r.Context(), o.PrincipalID)
			if e == nil && user.State == core.UserStateActive {
				eligible, err = a.canProject(withIdentity(r.Context(), identityForUser(user)), core.PermissionProjectView, app.ProjectID)
			}
		case core.PrincipalTeam:
			if _, e := a.store.GetTeam(r.Context(), o.PrincipalID); e == nil {
				if currentIdentity(r.Context()).SystemRole == core.UserRoleOwner {
					eligible = true
				} else {
					grants, e := a.store.ListRoleAssignments(r.Context())
					if e != nil {
						a.internal(w, e)
						return
					}
					for _, g := range grants {
						if g.PrincipalType == core.PrincipalTeam && g.PrincipalID == o.PrincipalID && g.ScopeType == core.ScopeProject && g.ScopeID == app.ProjectID && (g.ExpiresAt == nil || g.ExpiresAt.After(time.Now())) {
							eligible = true
						}
					}
				}
			}
		}
		if err != nil {
			a.internal(w, err)
			return
		}
		if !eligible {
			problem(w, 400, "Invalid owner", "Choose an active user or team with access to this project.")
			return
		}
	}
	if err := data.SaveApplicationOwner(r.Context(), o); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, o)
}
func (a *API) listTeamMappings(w http.ResponseWriter, r *http.Request) {
	data, ok := a.ops(w)
	if !ok {
		return
	}
	out, err := data.ListIdentityTeamMappings(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (a *API) saveTeamMapping(w http.ResponseWriter, r *http.Request) {
	data, ok := a.ops(w)
	if !ok {
		return
	}
	var m core.IdentityTeamMapping
	if !decode(w, r, &m) {
		return
	}
	m.ID = ulid.Make().String()
	m.ExternalGroup = strings.ToLower(strings.TrimSpace(m.ExternalGroup))
	parts := strings.Split(m.ExternalGroup, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || len(m.ExternalGroup) > 200 {
		problem(w, 400, "Invalid group", "Use a GitHub organization/team-slug.")
		return
	}
	if _, err := a.store.GetAuthProvider(r.Context(), m.ProviderID); err != nil {
		problem(w, 400, "Invalid provider", "Choose a sign-in provider.")
		return
	}
	if _, err := a.store.GetTeam(r.Context(), m.TeamID); err != nil {
		problem(w, 400, "Invalid team", "Choose a Dispatch team.")
		return
	}
	if err := data.SaveIdentityTeamMapping(r.Context(), m); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 201, m)
}
func (a *API) removeTeamMapping(w http.ResponseWriter, r *http.Request) {
	data, ok := a.ops(w)
	if !ok {
		return
	}
	if err := data.DeleteIdentityTeamMapping(r.Context(), chi.URLParam(r, "id")); err != nil {
		a.internal(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *API) retentionProject(w http.ResponseWriter, r *http.Request) bool {
	return a.requireProject(w, r, core.PermissionProjectManage, chi.URLParam(r, "id"))
}
func (a *API) getRetention(w http.ResponseWriter, r *http.Request) {
	if !a.retentionProject(w, r) {
		return
	}
	data, ok := a.ops(w)
	if !ok {
		return
	}
	p, err := data.GetRetentionPolicy(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, p)
}
func (a *API) saveRetention(w http.ResponseWriter, r *http.Request) {
	if !a.retentionProject(w, r) {
		return
	}
	data, ok := a.ops(w)
	if !ok {
		return
	}
	var p core.RetentionPolicy
	if !decode(w, r, &p) {
		return
	}
	p.ProjectID = chi.URLParam(r, "id")
	if p.LogDays < 1 || p.RunDays < 1 || p.KeepRuns < 5 || p.LogDays > 36500 || p.RunDays > 36500 || p.KeepRuns > 10000 {
		problem(w, 400, "Invalid retention", "Keep at least five runs and at least one day of history and logs.")
		return
	}
	if err := data.SaveRetentionPolicy(r.Context(), p); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, p)
}
func (a *API) previewRetention(w http.ResponseWriter, r *http.Request) { a.runRetention(w, r, false) }
func (a *API) applyRetention(w http.ResponseWriter, r *http.Request)   { a.runRetention(w, r, true) }
func (a *API) runRetention(w http.ResponseWriter, r *http.Request, apply bool) {
	if !a.retentionProject(w, r) {
		return
	}
	data, ok := a.ops(w)
	if !ok {
		return
	}
	p, err := data.GetRetentionPolicy(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	if apply {
		var input struct {
			Confirm string `json:"confirm"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.Confirm != p.ProjectID {
			problem(w, 400, "Confirmation required", "Confirm the selected project before removing history.")
			return
		}
	}
	result, err := data.ApplyRetention(r.Context(), p, apply, time.Now().UTC())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *API) ownerCandidates(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "id")
	if !a.requireProject(w, r, core.PermissionProjectConfigure, project) {
		return
	}
	users, err := a.store.ListUsers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	teams, err := a.store.ListTeams(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	assignments, err := a.store.ListRoleAssignments(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	userRows := []map[string]string{}
	teamRows := []map[string]string{}
	for _, u := range users {
		if u.State != core.UserStateActive {
			continue
		}
		allowed, err := a.canProject(withIdentity(r.Context(), identityForUser(u)), core.PermissionProjectView, project)
		if err != nil {
			a.internal(w, err)
			return
		}
		if allowed {
			userRows = append(userRows, map[string]string{"id": u.ID, "username": u.Username, "displayName": u.DisplayName})
		}
	}
	eligible := map[string]bool{}
	for _, grant := range assignments {
		if grant.PrincipalType == core.PrincipalTeam && grant.ScopeID == project && grant.ScopeType == core.ScopeProject && (grant.ExpiresAt == nil || grant.ExpiresAt.After(time.Now())) {
			eligible[grant.PrincipalID] = true
		}
	}
	for _, team := range teams {
		if eligible[team.ID] || currentIdentity(r.Context()).SystemRole == core.UserRoleOwner {
			teamRows = append(teamRows, map[string]string{"id": team.ID, "name": team.Name})
		}
	}
	writeJSON(w, 200, map[string]any{"users": userRows, "teams": teamRows})
}
