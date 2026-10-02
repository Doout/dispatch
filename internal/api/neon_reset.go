package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflow"
	"github.com/oklog/ulid/v2"
	"io"
	"net/http"
	"strings"
	"time"
)

type neonLifecycleReviewKey struct{}
type neonLifecycleSnapshot struct {
	Record   core.ServiceResource
	Template core.WorkflowResource
	Spec     workflow.ServiceTemplateSpec
}

func (a *API) neonResetTemplate(ctx context.Context, record core.ServiceResource) (core.WorkflowResource, workflow.ServiceTemplateSpec, error) {
	run, err := a.store.GetServiceProvisionRun(ctx, record.RunID)
	if err != nil {
		return core.WorkflowResource{}, workflow.ServiceTemplateSpec{}, err
	}
	resource, spec, err := a.workflows.PreviewServiceTemplate(ctx, record.ProjectID, run.TemplateID)
	if err != nil {
		return resource, spec, err
	}
	if spec.Provision.Neon.DataMode != "" && spec.Provision.Neon.DataMode != "schema-only" {
		return resource, spec, errors.New("reset requires a schema-only template")
	}
	return resource, spec, nil
}

func (a *API) resetNeonPreview(w http.ResponseWriter, r *http.Request) {
	record, ok := a.serviceResourceRecord(w, r, core.PermissionProjectConfigure)
	if !ok {
		return
	}
	if !a.requireProject(w, r, core.PermissionDeploymentRun, record.ProjectID) {
		return
	}
	snapshot, reviewed := r.Context().Value(neonLifecycleReviewKey{}).(neonLifecycleSnapshot)
	if !reviewed || snapshot.Record.RunID != record.RunID || snapshot.Record.Revision != record.Revision {
		problem(w, 409, "Reset changed", "Review the current database again.")
		return
	}
	resource, spec := snapshot.Template, snapshot.Spec
	if spec.Provision.Neon == nil {
		problem(w, 409, "Reset unavailable", "Select an active schema-only template in this project.")
		return
	}
	accepted, _, err := a.loadAcceptedServiceResource(r.Context(), record)
	if err != nil {
		a.internal(w, err)
		return
	}
	target, err := a.serviceProvisionTarget(r.Context(), spec, record.ProjectID)
	if err != nil {
		problem(w, 409, "Reset unavailable", err.Error())
		return
	}
	run := core.ServiceProvisionRun{ID: ulid.Make().String(), ProjectID: record.ProjectID, TemplateID: resource.ID, Target: target, State: "queued", CreatedAt: time.Now().UTC()}
	if claim, ok := core.MutationAcceptanceFromContext(r.Context()); ok {
		run.ID = claim.OperationID
	}
	run.ServiceName = "preview-" + strings.ToLower(run.ID)
	target.ResourceName = "neon-" + strings.ToLower(run.ID)
	ctx := context.WithValue(r.Context(), neonPreviewAcceptanceKey{}, neonPreviewAcceptance{ID: record.PreviewID, Alias: record.PreviewAlias, ReplacesRunID: record.RunID, ReplacesRevision: record.Revision, Generation: accepted.Neon.Scope.Generation + 1})
	if err = a.captureServiceResource(ctx, resource, spec, "Schema-only replacement; previous branch retained", nil, run, nil); err != nil {
		problem(w, 409, "Preview changed", "Pause the preview and finish active runs or cleanup before reviewing reset again.")
		return
	}
	core.RecordAcceptedOperation(r.Context(), run.ID)
	destructiveOutcome(r, "accepted")
	go a.executeOwnedServiceResource(run.ID, true)
	next, err := a.store.(store.ServiceResourceStore).GetServiceResource(r.Context(), run.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	if claim, ok := core.MutationAcceptanceFromContext(r.Context()); ok {
		receipt, e := a.store.(store.MutationReceiptStore).GetMutationReceipt(r.Context(), claim.ReceiptID)
		if e != nil {
			a.internal(w, e)
			return
		}
		a.writeMutationReceipt(w, r, receipt, 202)
		return
	}
	writeJSON(w, 202, next)
}

func (a *API) resetNeonPreviewRequest(w http.ResponseWriter, r *http.Request) {
	record, ok := a.serviceResourceRecord(w, r, core.PermissionProjectConfigure)
	if !ok {
		return
	}
	if !a.requireProject(w, r, core.PermissionDeploymentRun, record.ProjectID) {
		return
	}
	var input struct {
		Confirmation destructiveConfirmation `json:"confirmation"`
	}
	if !decode(w, r, &input) {
		return
	}
	r, receipt, proceed := a.reserveMutation(w, r, record.ProjectID, "service.neon.reset", input, "service_provision", record.RunID)
	if !proceed {
		return
	}
	raw, _ := json.Marshal(input)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	a.confirmDestructiveAction("neon-service", "reset", a.resetNeonPreview).ServeHTTP(w, r)
	if receipt != nil {
		a.failMutationAcceptance(r.Context(), 409, "Reset was not accepted; inspect the preview and original run before reviewing again.")
	}
}
