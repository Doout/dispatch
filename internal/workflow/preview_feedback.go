package workflow

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

func (s *Service) previewPullRequests(ctx context.Context, resource core.WorkflowResource, sources map[string]core.WorkflowSourceRevision) ([]core.WorkflowPullRequest, error) {
	if !resource.Temporary {
		return nil, nil
	}
	triggers, err := s.Store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return nil, err
	}
	targets := []core.WorkflowPullRequest{}
	seen := map[string]bool{}
	for _, trigger := range triggers {
		if trigger.ResourceID != resource.ID || trigger.ClosedAt != nil {
			continue
		}
		connection, err := s.Store.GetGitHubApp(ctx, trigger.GitHubAppID)
		if err != nil {
			return nil, err
		}
		for alias, source := range sources {
			number := trigger.LinkedPullRequests[alias]
			repo := normalizeRepository(source.Repository)
			if repo == normalizeRepository(trigger.Repository) {
				number = trigger.PullRequestNumber
			}
			if number < 1 || source.CommitSHA == "" || trigger.GitHubAppID == "" {
				continue
			}
			key := trigger.GitHubAppID + ":" + repo + ":" + strconv.Itoa(number) + ":" + source.CommitSHA
			if seen[key] {
				continue
			}
			seen[key] = true
			target := core.WorkflowPullRequest{GitHubAppID: trigger.GitHubAppID, Repository: repo, Number: number, CommitSHA: source.CommitSHA}
			if connection.WebURL != "" {
				target.URL = fmt.Sprintf("%s/%s/pull/%d", strings.TrimRight(connection.WebURL, "/"), repo, number)
			}
			targets = append(targets, target)
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Repository < targets[j].Repository })
	return targets, nil
}

func (s *Service) previewFeedback(ctx context.Context, resource core.WorkflowResource, deployed core.WorkflowRevision, document Document) (*core.WorkflowFeedback, error) {
	targets := deployed.PullRequests
	// Old deployments did not capture linked PR identities. Report their exact
	// commits, but require a new deployment before enabling PR reviews.
	legacy := len(targets) == 0
	if legacy {
		var err error
		targets, err = s.previewPullRequests(ctx, resource, deployed.Sources)
		if err != nil {
			return nil, err
		}
	}
	if len(targets) == 0 {
		return nil, nil
	}
	feedback := &core.WorkflowFeedback{DeploymentID: deployed.ID, WorkflowReporting: core.WorkflowReporting{StatusContext: "Dispatch/preview-tests/" + resource.Name}}
	if document.Spec.Reporting != nil {
		feedback.WorkflowReporting = *document.Spec.Reporting
		if feedback.StatusContext == "" {
			feedback.StatusContext = "Dispatch/preview-tests/" + resource.Name
		}
	}
	triggers, err := s.Store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return nil, err
	}
	for _, trigger := range triggers {
		if trigger.ResourceID == resource.ID && trigger.ClosedAt == nil {
			feedback.PreviewURL = trigger.PreviewURL
			break
		}
	}
	for _, target := range targets {
		result := core.WorkflowFeedbackTarget{WorkflowPullRequest: target}
		if legacy && (feedback.ReviewOnSuccess == "approve" || feedback.ReviewOnFailure == "requestChanges") {
			result.Review = "skipped"
			result.SkipReason = "Redeploy this preview to capture the tested PR identities before enabling reviews."
		}
		feedback.Targets = append(feedback.Targets, result)
	}
	return feedback, nil
}
