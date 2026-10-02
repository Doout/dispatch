package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

// StartPreviewChecks runs on-demand checks against the last completed preview
// deployment. It reuses that deployment's source revisions and skips build and
// Helm work entirely.
func (s *Service) StartPreviewChecks(ctx context.Context, resourceID, commentID string) (core.WorkflowRevision, error) {
	unlock := s.lock("schedule:" + resourceID)
	defer unlock()
	if existing, handled, err := s.receivedRevision(ctx, resourceID); handled || err != nil {
		return existing, err
	}
	resource, err := s.Store.GetWorkflowResource(ctx, resourceID)
	if err != nil {
		return core.WorkflowRevision{}, err
	}
	if !resource.Temporary || !resource.Active || resource.Kind != KindApplication {
		return core.WorkflowRevision{}, errors.New("preview is not active")
	}
	documents, err := Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].Spec == nil {
		return core.WorkflowRevision{}, errors.New("preview configuration is invalid")
	}
	revisions, err := s.Store.ListWorkflowRevisions(ctx, resourceID, 0)
	if err != nil {
		return core.WorkflowRevision{}, err
	}
	var deployed core.WorkflowRevision
	for _, revision := range revisions {
		if strings.HasPrefix(revision.Trigger, "pull request test ") {
			continue
		}
		if revision.State != "succeeded" && revision.State != "awaiting_approval" {
			return core.WorkflowRevision{}, errors.New("the latest preview deployment is not ready; run /preview first")
		}
		deployed = revision
		break
	}
	if deployed.ID == "" {
		return core.WorkflowRevision{}, errors.New("deploy this preview with /preview before running tests")
	}
	deployedStages, err := s.Store.ListWorkflowStageRuns(ctx, deployed.ID)
	if err != nil {
		return core.WorkflowRevision{}, err
	}
	ready := map[string]bool{}
	for _, stage := range deployedStages {
		ready[stage.StageName] = stage.State == "succeeded"
	}
	checks := 0
	for _, stage := range documents[0].Spec.Stages {
		if !ready[stage.Name] {
			continue
		}
		for _, check := range stage.Checks {
			if check.When == "onDemand" {
				checks++
			}
		}
	}
	if checks == 0 {
		return core.WorkflowRevision{}, errors.New("this preview has no on-demand checks on a ready stage")
	}
	// A repeated test command replaces older checks without touching a
	// deployment waiting for approval of a later stage.
	ids, err := s.Store.SupersedeWorkflowTestRevisions(ctx, resourceID)
	if err != nil {
		return core.WorkflowRevision{}, err
	}
	for _, id := range ids {
		s.mu.Lock()
		cancel := s.runCancels[id]
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	revision := core.WorkflowRevision{ID: ulid.Make().String(), ResourceID: resourceID, ConfigSHA: deployed.ConfigSHA, SpecDigest: deployed.SpecDigest,
		State: "queued", Trigger: "pull request test " + commentID, Sources: deployed.Sources, Outputs: deployed.Outputs,
		CreatedAt: time.Now().UTC(), PullRequests: deployed.PullRequests}
	revision.Feedback, err = s.previewFeedback(ctx, resource, deployed, documents[0])
	if err != nil {
		return core.WorkflowRevision{}, err
	}
	source, err := s.Store.GetConfigSource(ctx, resource.ConfigSourceID)
	if err != nil {
		return revision, err
	}
	revision.Checks, err = s.workflowCheckReports(ctx, resource, source, revision, true)
	if err != nil {
		return revision, err
	}
	revision.SourceTrust, err = s.SourceTrust(ctx, resource, revision)
	if err != nil {
		now := time.Now().UTC()
		revision.State, revision.Error, revision.FinishedAt = "failed", err.Error(), &now
		if saveErr := s.Store.CreateWorkflowRevision(ctx, revision); saveErr != nil {
			return revision, saveErr
		}
		return revision, err
	}
	if err := s.Store.CreateWorkflowRevision(ctx, revision); err != nil {
		return core.WorkflowRevision{}, err
	}
	s.launchRun(revision.ID, func(runCtx context.Context) {
		s.runPreviewChecks(runCtx, resource, documents[0], revision, ready)
	})
	return revision, nil
}

func (s *Service) runPreviewChecks(ctx context.Context, resource core.WorkflowResource, document Document, revision core.WorkflowRevision, ready map[string]bool) {
	unlock := s.lock("resource:" + resource.ID)
	defer unlock()
	if ctx.Err() != nil {
		return
	}
	if err := s.checkRevisionTrust(ctx, revision); err != nil {
		s.finishPreviewChecks(ctx, &revision, err)
		return
	}
	now := time.Now().UTC()
	revision.State, revision.StartedAt = "running", &now
	if err := s.Store.UpdateWorkflowRevision(ctx, revision); err != nil {
		return
	}
	for _, stage := range document.Spec.Stages {
		if !ready[stage.Name] || ctx.Err() != nil {
			continue
		}
		names := []string{}
		for _, name := range sortedCheckNames(stage.Checks) {
			if stage.Checks[name].When == "onDemand" {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			continue
		}
		started := time.Now().UTC()
		run := core.WorkflowStageRun{ID: ulid.Make().String(), RevisionID: revision.ID, StageName: stage.Name, TargetRef: stage.TargetRef,
			State: "running", CheckRuns: map[string]string{}, DeploymentIDs: []string{}, CreatedAt: started, StartedAt: &started}
		if err := s.Store.CreateWorkflowStageRun(ctx, run); err != nil {
			s.finishPreviewChecks(ctx, &revision, err)
			return
		}
		for _, name := range names {
			if err := s.runStageCheck(ctx, resource, revision, stage, &run, name, stage.Checks[name]); err != nil {
				if ctx.Err() != nil {
					return
				}
				finished := time.Now().UTC()
				run.State, run.Error, run.FinishedAt = "failed", err.Error(), &finished
				_ = s.Store.UpdateWorkflowStageRun(context.Background(), run)
				s.finishPreviewChecks(ctx, &revision, fmt.Errorf("stage %s: %w", stage.Name, err))
				return
			}
		}
		finished := time.Now().UTC()
		run.State, run.FinishedAt = "succeeded", &finished
		if err := s.Store.UpdateWorkflowStageRun(ctx, run); err != nil {
			s.finishPreviewChecks(ctx, &revision, err)
			return
		}
	}
	if ctx.Err() == nil {
		s.finishPreviewChecks(ctx, &revision, nil)
	}
}

func (s *Service) finishPreviewChecks(ctx context.Context, revision *core.WorkflowRevision, cause error) {
	if ctx.Err() != nil {
		return
	}
	finished := time.Now().UTC()
	revision.FinishedAt = &finished
	if cause != nil {
		revision.State, revision.Error = "failed", cause.Error()
	} else {
		revision.State = "succeeded"
	}
	_ = s.Store.UpdateWorkflowRevision(context.Background(), *revision)
}
