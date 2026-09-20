package api

import (
	"errors"
	"net/http"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

type serviceImpactConsumer struct {
	core.ServiceConsumer
	ProjectID    string `json:"projectId"`
	TargetName   string `json:"targetName"`
	Environment  string `json:"environment"`
	DeploymentID string `json:"deploymentId,omitempty"`
	CanRedeploy  bool   `json:"canRedeploy"`
	Reason       string `json:"reason,omitempty"`
}

func (a *API) serviceImpact(w http.ResponseWriter, r *http.Request) {
	service, ok := a.loadService(w, r, core.PermissionProjectView)
	if !ok {
		return
	}
	consumers, err := a.store.ListServiceConsumers(r.Context(), service.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	result := []serviceImpactConsumer{}
	for _, c := range consumers {
		app, err := a.store.GetApp(r.Context(), c.AppID)
		if err != nil {
			a.internal(w, err)
			return
		}
		if app.ProjectID != service.ProjectID {
			continue
		}
		v := serviceImpactConsumer{ServiceConsumer: c, ProjectID: app.ProjectID, Environment: app.HelmNamespace}
		if server, err := a.store.GetServer(r.Context(), app.ServerID); err == nil {
			v.TargetName = server.Name
		}
		allowed, err := a.canProject(r.Context(), core.PermissionDeploymentRun, app.ProjectID)
		if err != nil {
			a.internal(w, err)
			return
		}
		latest, err := a.store.LatestSuccessfulDeployment(r.Context(), app.ID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			v.Reason = "Deploy the application once before rotating its runtime credentials."
		case err != nil:
			a.internal(w, err)
			return
		default:
			v.DeploymentID = latest.ID
			matching, e := a.deploymentAppInputsMatch(r.Context(), app, latest)
			if e != nil {
				a.internal(w, e)
				return
			}
			v.CanRedeploy = allowed && !app.Template && matching
			if !matching {
				v.Reason = "Application inputs changed. Review and deploy from the application."
			}
			if !allowed {
				v.Reason = "Deployment permission is required."
			}
		}
		if active, err := a.store.ActiveDeploymentForApp(r.Context(), app.ID); err != nil {
			a.internal(w, err)
			return
		} else if active != nil {
			v.CanRedeploy = false
			v.Reason = "A deployment is already active."
		}
		result = append(result, v)
	}
	writeJSON(w, 200, map[string]any{"serviceId": service.ID, "revision": service.Revision, "consumers": result})
}
func (a *API) redeployServiceConsumers(w http.ResponseWriter, r *http.Request) {
	service, ok := a.loadService(w, r, core.PermissionProjectConfigure)
	if !ok {
		return
	}
	if !a.requireProject(w, r, core.PermissionDeploymentRun, service.ProjectID) {
		return
	}
	var input struct {
		Revision int64    `json:"revision"`
		AppIDs   []string `json:"appIds"`
		Confirm  bool     `json:"confirm"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !input.Confirm || len(input.AppIDs) == 0 || len(input.AppIDs) > 50 {
		problem(w, 400, "Choose consumers", "Explicitly confirm between one and 50 applications to redeploy.")
		return
	}
	if input.Revision != service.Revision {
		problem(w, 409, "Service changed", "Refresh the impact view before redeploying.")
		return
	}
	consumers, err := a.store.ListServiceConsumers(r.Context(), service.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	allowed := map[string]bool{}
	for _, c := range consumers {
		allowed[c.AppID] = true
	}
	seen := map[string]bool{}
	sources := map[string]string{}
	reviews := map[string]core.DeploymentReview{}
	// Validate every selection before accepting any deployments.
	for _, id := range input.AppIDs {
		if !allowed[id] || seen[id] {
			problem(w, 400, "Invalid consumer", "Select each bound application once.")
			return
		}
		seen[id] = true
		app, err := a.store.GetApp(r.Context(), id)
		if err != nil {
			a.internal(w, err)
			return
		}
		if app.ProjectID != service.ProjectID || app.Template {
			problem(w, 400, "Invalid consumer", "Select a runtime application in this project.")
			return
		}
		latest, err := a.store.LatestSuccessfulDeployment(r.Context(), id)
		matching := false
		if err == nil {
			matching, err = a.deploymentAppInputsMatch(r.Context(), app, latest)
		}
		if err != nil || !matching {
			problem(w, 409, "Review application inputs", "One of the selected applications has changed or has no successful release. Deploy it from its application page.")
			return
		}
		active, err := a.store.ActiveDeploymentForApp(r.Context(), id)
		if err != nil {
			a.internal(w, err)
			return
		}
		if active != nil {
			problem(w, 409, "Application busy", "Wait for active deployments before rotating consumers.")
			return
		}
		review, err := a.reviewServiceConsumer(r.Context(), app)
		if err != nil {
			a.internal(w, err)
			return
		}
		// The selected credential revision was explicitly reviewed in the impact view.
		if expected, ok := review.ServiceRevisions[service.ID]; !ok || expected != input.Revision {
			problem(w, 409, "Service changed", "Refresh the impact view before redeploying.")
			return
		}
		reviews[id] = review
		sources[id] = latest.CommitSHA
	}
	type result struct {
		AppID      string           `json:"appId"`
		Deployment *core.Deployment `json:"deployment,omitempty"`
		Error      string           `json:"error,omitempty"`
	}
	out := []result{}
	for _, id := range input.AppIDs {
		d, err := a.deploy.StartReviewed(r.Context(), id, sources[id], reviews[id])
		v := result{AppID: id}
		if err != nil {
			v.Error = "Deployment was not accepted. Refresh the application before retrying."
		} else {
			v.Deployment = &d
		}
		out = append(out, v)
	}
	writeJSON(w, 202, out)
}
