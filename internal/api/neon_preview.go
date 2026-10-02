package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflow"
	"github.com/oklog/ulid/v2"
)

type neonPreviewAcceptanceKey struct{}
type neonPreviewAcceptance struct {
	ID, Alias, ReplacesRunID string
	ReplacesRevision         int64
	Generation               int64
}

// Prepare only after the workflow runner has checked this revision's source
// trust. Provisioning credentials are never inserted into its document or jobs.
func (a *API) prepareNeonPreviewServices(ctx context.Context, preview core.WorkflowResource, revision core.WorkflowRevision, document workflow.Document) error {
	if !preview.Temporary || document.Spec == nil {
		return errors.New("Neon preview databases require a temporary workflow")
	}
	source, err := a.store.GetConfigSource(ctx, preview.ConfigSourceID)
	if err != nil {
		return err
	}
	data := a.store.(store.NeonStore)
	aliases := make([]string, 0, len(document.Spec.PreviewServices))
	for alias := range document.Spec.PreviewServices {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		template := document.Spec.PreviewServices[alias]
		resource, spec, err := a.workflows.PreviewServiceTemplate(ctx, source.ProjectID, template.TemplateRef)
		if err != nil {
			return err
		}
		expectedScope := "preview-service:" + alias + ":" + resource.ID + ":" + resource.ConfigSHA + ":" + spec.Provision.Neon.ProviderRef
		if revision.SourceTrust != nil && (!revision.SourceTrust.Allowed || !slices.Contains(revision.SourceTrust.CredentialScope, expectedScope)) {
			return errors.New("preview database configuration changed after source approval; start a new revision")
		}
		runID, err := data.GetNeonPreviewService(ctx, preview.ID, alias)
		if errors.Is(err, store.ErrNotFound) {
			target, targetErr := a.serviceProvisionTarget(ctx, spec, source.ProjectID)
			if targetErr != nil {
				return targetErr
			}
			run := core.ServiceProvisionRun{ID: ulid.Make().String(), ProjectID: source.ProjectID, TemplateID: resource.ID, ServiceName: "preview-" + preview.ID + "-" + alias, Target: target, State: "queued", CreatedAt: time.Now().UTC()}
			run.ServiceName = "preview-" + strings.ToLower(run.ID)
			target.ResourceName = "neon-" + strings.ToLower(run.ID)
			acceptance := context.WithValue(ctx, neonPreviewAcceptanceKey{}, neonPreviewAcceptance{ID: preview.ID, Alias: alias})
			err = a.captureServiceResource(acceptance, resource, spec, "Owned database for preview "+preview.Name, nil, run, nil)
			if err != nil {
				runID, err = data.GetNeonPreviewService(ctx, preview.ID, alias)
			} else {
				runID, err = run.ID, nil
			}
		}
		if err != nil {
			return errors.New("preview database acceptance unavailable")
		}
		record, err := a.store.(store.ServiceResourceStore).GetServiceResource(ctx, runID)
		if err != nil {
			return err
		}
		accepted, _, err := a.loadAcceptedServiceResource(ctx, record)
		if err != nil || accepted.Neon == nil || accepted.Request.ConfigSHA != resource.ConfigSHA || accepted.Neon.Scope.PreviewID != preview.ID || record.PreviewAlias != alias {
			return errors.New("preview database configuration changed; retain this branch and review a replacement")
		}
		if record.State != "ready" {
			if !record.LeaseUntil.After(time.Now()) {
				go a.executeOwnedServiceResource(runID, !record.ProviderCreateAttempted)
			}
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			waiting := time.NewTimer(6 * time.Minute)
			defer waiting.Stop()
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-waiting.C:
					return errors.New("preview database is still pending; inspect its service run")
				case <-ticker.C:
				}
				record, err = a.store.(store.ServiceResourceStore).GetServiceResource(ctx, runID)
				if err != nil {
					return err
				}
				if record.State == "ready" {
					break
				}
				if record.State == "unresolved" && !record.LeaseUntil.After(time.Now()) {
					return fmt.Errorf("preview database %s requires service-run recovery", alias)
				}
				if record.State == "deleted" {
					return errors.New("preview database was explicitly deleted; create a new preview")
				}
			}
		}
	}
	current, err := a.store.GetWorkflowResource(ctx, preview.ID)
	if err != nil || !current.Active {
		return errors.New("preview closed while its database was being prepared")
	}
	return ctx.Err()
}
