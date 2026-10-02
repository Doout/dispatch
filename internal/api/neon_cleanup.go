package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

func (a *API) neonLifecycleRoutes(r chi.Router) {
	r.Post("/resource/reset-preview", a.previewDestructiveAction("neon-service", "reset"))
	r.Post("/resource/reset", a.resetNeonPreviewRequest)
	for _, policy := range []string{"retain", "suspend", "delete"} {
		action := "policy-" + policy
		a.destructiveRoute(r, "POST", "/resource/"+action, "neon-service", action, a.setNeonCleanupPolicy)
	}
}
func (a *API) neonLifecycleReview(ctx context.Context, r *http.Request, action string) (destructiveReview, error) {
	id := chi.URLParam(r, "id")
	record, err := a.store.(store.ServiceResourceStore).GetServiceResource(ctx, id)
	if err != nil {
		return destructiveReview{}, err
	}
	out := destructiveReview{ResourceID: id, ResourceType: "neon-service", Action: action, ProjectID: record.ProjectID, Name: record.Name, Resources: []string{"Owned branch: " + record.ResourceID, "Preview: " + record.PreviewID + " / " + record.PreviewAlias}}
	if record.Target.Provider != "neon" || record.PreviewID == "" {
		return out, errors.New("this action requires an owned Neon preview database")
	}
	accepted, _, err := a.loadAcceptedServiceResource(ctx, record)
	if err != nil {
		return out, err
	}
	inspected, err := a.inspectNeonResource(ctx, accepted)
	if err != nil {
		return out, err
	}
	if record.State != "ready" || inspected.State != "ready" || record.LeaseUntil.After(time.Now()) {
		out.BlockedReason = "The database must be ready with no active operation before changing its lifecycle."
	}
	cleanups, err := a.store.ListWorkflowPreviewCleanups(ctx, record.PreviewID)
	if err != nil {
		return out, err
	}
	for _, c := range cleanups {
		if c.State != "succeeded" {
			out.BlockedReason = "Preview cleanup already captured its policy. Reconcile that operation first."
		}
	}
	snapshot := neonLifecycleSnapshot{Record: record}
	templateDigest := ""
	policy := strings.TrimPrefix(action, "policy-")
	out.StoragePolicy = policy
	switch policy {
	case "reset":
		template, spec, e := a.neonResetTemplate(ctx, record)
		if e != nil {
			return out, e
		}
		snapshot.Template, snapshot.Spec = template, spec
		templateDigest = template.ConfigSHA
		preview, e := a.store.GetWorkflowResource(ctx, record.PreviewID)
		if e != nil {
			return out, e
		}
		if preview.Active {
			out.BlockedReason = "Pause this preview and finish active runs before resetting its database."
		}
		out.StoragePolicy = "retain"
		out.Summary = "Create a new schema-only generation with the current template. Parent rows are never copied. Keep the old branch and connection until the replacement binding is committed; existing deployments keep their old connection until redeployed. Failed replacement blocks preview resume until reconciled or its candidate is explicitly deleted."
		out.Resources = append(out.Resources, "Current schema-only template: "+template.ID+" / "+template.ConfigSHA)
	case "retain":
		out.Summary = "Retain this branch and its data when the preview closes or expires. Deleting its data requires a separate reviewed action."
	case "suspend":
		out.Summary = "Suspend this branch's compute after preview workloads stop on close or expiry. Data remains; a new connection can wake compute."
	case "delete":
		out.Summary = "Permanently delete this owned branch and all of its data after preview workloads stop on close or expiry. Cleanup detaches this preview's closed applications; other consumers block deletion."
	default:
		return out, errors.New("unsupported Neon lifecycle action")
	}
	raw, _ := json.Marshal(struct {
		Record         core.ServiceResource
		Inspection     core.ServiceResourceInspection
		Action         string
		TemplateDigest string
	}{record, inspected, action, templateDigest})
	*r = *r.WithContext(context.WithValue(r.Context(), neonLifecycleReviewKey{}, snapshot))
	out.Version = fmt.Sprintf("%x", sha256.Sum256(raw))
	return out, nil
}
func (a *API) setNeonCleanupPolicy(w http.ResponseWriter, r *http.Request) {
	record, ok := a.serviceResourceRecord(w, r, core.PermissionProjectConfigure)
	if !ok {
		return
	}
	if !a.requireProject(w, r, core.PermissionDeploymentRun, record.ProjectID) {
		return
	}
	confirmation, _ := r.Context().Value(destructiveConfirmationKey{}).(destructiveConfirmation)
	policy := strings.TrimPrefix(confirmation.Action, "policy-")
	actor := currentIdentity(r.Context())
	kind := actor.Kind
	if kind == "" {
		kind = "user"
	}
	snapshot, reviewed := r.Context().Value(neonLifecycleReviewKey{}).(neonLifecycleSnapshot)
	if !reviewed || snapshot.Record.RunID != record.RunID || snapshot.Record.Revision != record.Revision {
		problem(w, 409, "Policy changed", "Review the current database again.")
		return
	}
	updated, err := a.store.(store.NeonLifecycleStore).SetNeonServicePolicy(r.Context(), record.RunID, record.Revision, policy, kind+":"+actor.ID, time.Now().UTC())
	if err != nil {
		problem(w, 409, "Policy changed", "Review the current database and preview cleanup state again.")
		return
	}
	destructiveOutcome(r, "succeeded")
	writeJSON(w, 200, updated)
}

func (a *API) reconcileNeonPreviewCleanup(ctx context.Context, c core.WorkflowPreviewCleanup) error {
	var joined error
	data := a.store.(store.NeonLifecycleStore)
	for _, entry := range c.Services {
		if entry.State == "succeeded" {
			continue
		}
		if entry.Policy == "retain" {
			entry.State = "succeeded"
			entry.Error = "Database retained by the selected policy."
			joined = errors.Join(joined, data.SaveNeonCleanupService(ctx, c, entry))
			continue
		}
		err := a.runNeonCleanupEntry(ctx, c, &entry)
		entry.State, entry.Error = "succeeded", ""
		if err != nil {
			entry.State = "blocked"
			entry.Error = "Owned database cleanup requires inspection; no uncertain provider action was repeated."
			joined = errors.Join(joined, err)
		}
		save, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		joined = errors.Join(joined, data.SaveNeonCleanupService(save, c, entry))
		done()
	}
	return joined
}
func (a *API) runNeonCleanupEntry(ctx context.Context, c core.WorkflowPreviewCleanup, entry *core.WorkflowPreviewCleanupService) error {
	data := a.store.(store.ServiceResourceStore)
	record, err := data.GetServiceResource(ctx, entry.RunID)
	if err != nil {
		return err
	}
	if record.Target.Provider != "neon" || record.PreviewID != c.ResourceID || record.PreviewAlias != entry.Alias || record.ResourceID != entry.ResourceID || record.Policy != entry.Policy {
		return errors.New("captured Neon cleanup identity changed")
	}
	if record.LeaseUntil.After(time.Now()) {
		return errors.New("original Neon operation has not reached its recovery deadline")
	}
	accepted, _, err := a.loadAcceptedServiceResource(ctx, record)
	if err != nil {
		return err
	}
	client, err := a.neonClient(accepted)
	if err != nil {
		return err
	}
	observed, err := client.Inspect(ctx, accepted.Neon.Spec, accepted.Neon.Scope)
	if err != nil {
		return err
	}
	done := entry.Policy == "delete" && observed.State == "absent" || entry.Policy == "suspend" && (observed.State == "absent" || observed.Endpoint.State == "idle")
	if entry.ActionStarted && !done {
		return errors.New("provider action outcome remains uncertain; inspect the captured branch before an explicit reviewed retry")
	}
	if record.State == "deleted" {
		if done {
			return nil
		}
		return errors.New("deleted Neon branch appeared again")
	}
	run, err := a.store.GetServiceProvisionRun(ctx, record.RunID)
	if err != nil {
		return err
	}
	// Claim even an already absent resource so registration cleanup remains atomic
	// and consumer checks still apply. A prior action is never posted again.
	if !entry.ActionStarted {
		record, err = a.store.(store.NeonLifecycleStore).ClaimNeonCleanupService(ctx, c, *entry)
		if err != nil {
			return err
		}
		entry.ActionStarted = true
		if !done {
			if entry.Policy == "delete" {
				err = client.Delete(ctx, accepted.Neon.Spec, accepted.Neon.Scope, entry.ResourceID)
			} else {
				err = client.Suspend(ctx, accepted.Neon.Spec, accepted.Neon.Scope, entry.ResourceID)
			}
			if err == nil {
				observed, err = client.Inspect(ctx, accepted.Neon.Spec, accepted.Neon.Scope)
				done = err == nil && (entry.Policy == "delete" && observed.State == "absent" || entry.Policy == "suspend" && observed.Endpoint.State == "idle")
			}
		}
	} else {
		// Recover only after provider evidence proved completion. Reclaim the original
		// service lease; this branch does not issue a provider mutation.
		state, operation := "deleting", entry.OperationID
		if entry.Policy == "suspend" {
			state, operation = "recovering", record.RunID
		}
		record, err = data.ClaimServiceResource(ctx, record.RunID, record.Revision, operation, state, c.LeaseToken, time.Now().UTC(), record.ResourceID)
		if err != nil {
			return err
		}
	}
	record.State, record.Message = "unresolved", "Neon cleanup outcome is uncertain; inspect the original branch."
	if entry.Policy == "delete" {
		record.State = "deleting"
	}
	if done && err == nil {
		record.State = "ready"
		record.Message = "Neon compute suspended; branch data retained."
		if entry.Policy == "delete" {
			record.State = "deleted"
			record.Message = "Owned Neon branch and data deleted by the reviewed preview policy."
		}
	}
	save, finish := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer finish()
	saveErr := data.SaveServiceResource(save, record, run, nil)
	if err != nil || !done {
		return errors.Join(err, saveErr, errors.New("Neon cleanup outcome is unresolved"))
	}
	return saveErr
}
