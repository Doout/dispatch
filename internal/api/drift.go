package api

import (
	"context"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/observe"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"net/http"
	"time"
)

type configurationSync struct {
	State        string     `json:"state"`
	Message      string     `json:"message"`
	SourceID     string     `json:"sourceId,omitempty"`
	LastSyncedAt *time.Time `json:"lastSyncedAt,omitempty"`
}
type revisionStatus struct {
	State    string `json:"state"`
	Applied  string `json:"applied,omitempty"`
	Observed string `json:"observed,omitempty"`
}
type applicationSync struct {
	ObservationFreshness         string             `json:"observationFreshness,omitempty"`
	ObservationChecking          bool               `json:"observationChecking,omitempty"`
	ObservationStaleAfterSeconds int                `json:"observationStaleAfterSeconds,omitempty"`
	AppID                        string             `json:"appId"`
	DeploymentID                 string             `json:"deploymentId,omitempty"`
	Configuration                configurationSync  `json:"configuration"`
	Revision                     revisionStatus     `json:"revision"`
	Drift                        core.DriftCheck    `json:"drift"`
	Supported                    bool               `json:"supported"`
	ReapplyAvailable             bool               `json:"reapplyAvailable"`
	Actions                      []core.DriftAction `json:"actions"`
}

func (a *API) applicationSync(ctx context.Context, id string) (applicationSync, error) {
	app, err := a.store.GetApp(ctx, id)
	if err != nil {
		return applicationSync{}, err
	}
	out := applicationSync{AppID: id, Configuration: configurationSync{State: "not_applicable", Message: "Application configuration is managed in Dispatch."}, Revision: revisionStatus{State: "not_deployed"}, Drift: core.DriftCheck{State: "unknown", Health: "unknown", Location: "Dispatch controller", Message: "No successful deployment is available.", Resources: []core.DriftResource{}}, Actions: []core.DriftAction{}}
	if a.observations != nil {
		if observation, e := a.observations.Status(ctx, id); e == nil {
			out.ObservationFreshness = observation.Freshness
			out.ObservationChecking = observation.Checking
			out.ObservationStaleAfterSeconds = observation.Configuration.StaleAfterSeconds
		}
	}
	server, err := a.store.GetServer(ctx, app.ServerID)
	if err != nil {
		return out, err
	}
	out.Supported = !app.Template && app.BuildType == core.BuildTypeHelm && server.Kubernetes != nil
	if !out.Supported {
		out.Drift.Message = "Runtime drift checks support Helm applications on Kubernetes and OpenShift."
	}
	d, err := a.store.LatestSuccessfulDeployment(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.DeploymentID = d.ID
	out.Revision = revisionStatus{State: "current", Applied: d.CommitSHA}
	matching, err := a.deploymentAppInputsMatch(ctx, app, d)
	if err != nil {
		return out, err
	}
	if !matching {
		out.Revision.State = "redeployment_required"
	}
	if old, e := a.store.GetDriftCheck(ctx, id); e == nil && old.DeploymentID == d.ID {
		out.Drift = old
	} else if e != nil && !errors.Is(e, store.ErrNotFound) {
		return out, e
	} else if out.Supported {
		out.Drift.Message = "This successful deployment has not been checked."
	}
	if _, e := a.store.GetDriftBaseline(ctx, d.ID); e == nil {
		out.ReapplyAvailable = out.Supported
	} else if errors.Is(e, store.ErrNotFound) && out.Supported && out.Drift.CheckedAt == nil {
		out.Drift.Message = "Not checked yet. Check now to inspect the retained Helm release and live resources."
	} else if e != nil && !errors.Is(e, store.ErrNotFound) {
		return out, e
	}
	active, err := a.store.ActiveDeploymentForApp(ctx, id)
	if err != nil {
		return out, err
	}
	if active != nil {
		out.ReapplyAvailable = false
		out.Revision.State = "deploying"
		out.Drift.State, out.Drift.Health, out.Drift.Message = "unknown", "unknown", "Deployment in progress. Check again after it finishes."
	}
	if d.Snapshot.TargetID != "" && d.Snapshot.TargetID != app.ServerID {
		out.ReapplyAvailable = false
		out.Drift.State, out.Drift.Health, out.Drift.Message = "unknown", "unknown", "The application target changed. Deploy to the new target first."
	}
	runs, err := a.store.ListWorkflowStageRuns(ctx, "")
	if err != nil {
		return out, err
	}
	for _, run := range runs {
		matches := false
		for _, dep := range run.DeploymentIDs {
			if dep == d.ID {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		revision, e := a.store.GetWorkflowRevision(ctx, run.RevisionID)
		if e != nil {
			return out, e
		}
		resource, e := a.store.GetWorkflowResource(ctx, revision.ResourceID)
		if e != nil {
			return out, e
		}
		source, e := a.store.GetConfigSource(ctx, resource.ConfigSourceID)
		if e != nil {
			return out, e
		}
		if source.ProjectID != app.ProjectID {
			return out, errors.New("configuration project mismatch")
		}
		out.Configuration = configurationSync{State: source.State, Message: "Last observed repository configuration.", SourceID: source.ID, LastSyncedAt: source.LastSyncedAt}
		if !source.Active || !resource.Active {
			out.Configuration.State = "paused"
		}
		if resource.State == "invalid" {
			out.Configuration.State = "invalid"
			out.Configuration.Message = "The application's repository configuration could not be synchronized."
		}
		out.Revision.Applied = revision.ID
		candidates, e := a.store.ListWorkflowRevisions(ctx, resource.ID, 1)
		if e != nil {
			return out, e
		}
		if len(candidates) > 0 {
			out.Revision.Observed = candidates[0].ID
			if candidates[0].ID != revision.ID && out.Revision.State != "deploying" {
				// A content-identical workflow can intentionally reuse the deployed revision.
				reused := false
				for _, candidateRun := range runs {
					if candidateRun.RevisionID == candidates[0].ID && candidateRun.StageName == run.StageName {
						for _, dep := range candidateRun.DeploymentIDs {
							if dep == d.ID {
								reused = true
							}
						}
					}
				}
				if !reused {
					out.Revision.State = "newer_revision_available"
				}
			}
		}
		break
	}
	bindings, err := a.store.GetAppServiceBindings(ctx, id)
	if err != nil {
		return out, err
	}
	for _, binding := range bindings {
		consumers, e := a.store.ListServiceConsumers(ctx, binding.ServiceRef)
		if e != nil {
			return out, e
		}
		for _, consumer := range consumers {
			if consumer.AppID == id && consumer.RedeploymentRequired && out.Revision.State == "current" {
				out.Revision.State = "redeployment_required"
			}
		}
	}
	out.Actions, err = a.store.ListDriftActions(ctx, id)
	return out, err
}
func (a *API) getApplicationSync(w http.ResponseWriter, r *http.Request) {
	result, err := a.applicationSync(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (a *API) checkApplicationDrift(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	status, err := a.applicationSync(r.Context(), id)
	if err != nil {
		a.internal(w, err)
		return
	}
	if !status.Supported {
		problem(w, http.StatusBadRequest, "Unsupported target", "Runtime checks require a Helm application on Kubernetes or OpenShift.")
		return
	}
	if a.observations != nil {
		_, err = a.observations.Check(r.Context(), id, "manual")
	} else {
		err = a.deploy.WithIdleApplication(r.Context(), id, func() error { _, err := a.drift.Check(r.Context(), id); return err })
	}
	if errors.Is(err, deploy.ErrDeploymentActive) || errors.Is(err, observe.ErrBusy) {
		problem(w, http.StatusConflict, "Application busy", "Wait for the deployment or runtime operation to finish.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	a.getApplicationSync(w, r)
}
func (a *API) reapplyApplication(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DeploymentID string `json:"deploymentId"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.DeploymentID == "" {
		problem(w, http.StatusBadRequest, "Deployment required", "Select the successful deployment shown in the drift check.")
		return
	}
	id := chi.URLParam(r, "id")
	status, err := a.applicationSync(r.Context(), id)
	if err != nil {
		a.internal(w, err)
		return
	}
	if !status.Supported || !status.ReapplyAvailable {
		problem(w, http.StatusConflict, "Reapply unavailable", "A saved baseline for the current successful deployment and an idle application are required.")
		return
	}
	err = a.deploy.WithIdleApplication(r.Context(), id, func() error {
		_, err := a.drift.Reapply(r.Context(), id, input.DeploymentID, currentIdentity(r.Context()).ID)
		return err
	})
	if errors.Is(err, deploy.ErrDeploymentActive) || errors.Is(err, observe.ErrBusy) {
		problem(w, http.StatusConflict, "Application busy", "Wait for the deployment or runtime operation to finish.")
		return
	}
	if err != nil {
		problem(w, http.StatusConflict, "Reapply stopped", err.Error())
		return
	}
	a.getApplicationSync(w, r)
}
