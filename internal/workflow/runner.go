package workflow

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func (s *Service) runApplication(ctx context.Context, resource core.WorkflowResource, source core.ConfigSource, revision core.WorkflowRevision) {
	unlock := s.lock("resource:" + resource.ID)
	defer unlock()
	now := time.Now().UTC()
	revision.State, revision.StartedAt = "running", &now
	if err := s.Store.UpdateWorkflowRevision(ctx, revision); err != nil {
		return
	}
	documents, err := Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].Spec == nil {
		s.failRevision(ctx, source, &revision, errors.New("stored application document is invalid"))
		return
	}
	document := documents[0]
	s.publishStatus(ctx, source, revision, "pending", "Deployment started")
	root, err := os.MkdirTemp("", "dispatch-workflow-")
	if err != nil {
		s.failRevision(ctx, source, &revision, err)
		return
	}
	defer os.RemoveAll(root)
	runtime := &jobRuntime{service: s, source: source, revision: revision, root: root, paths: map[string]string{}}
	defer runtime.close()
	revision.Outputs, err = runtime.executeJobs(ctx, resource, document.Spec.Jobs, document.Spec.Finally, false)
	if err != nil {
		s.failRevision(ctx, source, &revision, err)
		return
	}
	if err := s.Store.UpdateWorkflowRevision(ctx, revision); err != nil {
		s.failRevision(ctx, source, &revision, err)
		return
	}
	if err := s.runStages(ctx, resource, source, document, &revision, 0); err != nil {
		s.failRevision(ctx, source, &revision, err)
	}
}

func (s *Service) failRevision(ctx context.Context, source core.ConfigSource, revision *core.WorkflowRevision, cause error) {
	finished := time.Now().UTC()
	revision.State, revision.Error, revision.FinishedAt = "failed", cause.Error(), &finished
	_ = s.Store.UpdateWorkflowRevision(context.Background(), *revision)
	s.publishStatus(ctx, source, *revision, "failure", "Deployment failed")
}

func (s *Service) succeedRevision(ctx context.Context, source core.ConfigSource, revision *core.WorkflowRevision) {
	finished := time.Now().UTC()
	revision.State, revision.FinishedAt = "succeeded", &finished
	_ = s.Store.UpdateWorkflowRevision(context.Background(), *revision)
	s.publishStatus(ctx, source, *revision, "success", "Deployment succeeded")
}

func (s *Service) publishStatus(ctx context.Context, source core.ConfigSource, revision core.WorkflowRevision, state, description string) {
	if s.GitHub == nil || source.GitHubAppID == "" {
		return
	}
	seen := map[string]bool{}
	for _, item := range revision.Sources {
		key := normalizeRepository(item.Repository) + "@" + item.CommitSHA
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := s.GitHub.SetCommitStatus(ctx, source.GitHubAppID, item.Repository, item.CommitSHA, state, description, ""); err != nil && s.Logger != nil {
			s.Logger.Warn("commit status was not published", "repository", item.Repository, "error", err)
		}
	}
}
