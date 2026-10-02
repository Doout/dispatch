package workflow

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

// Check plans capture inputs, reporting identity and context before execution.
// They schedule only reporting; no job, health probe or QA execution starts here.
func (s *Service) workflowCheckReports(ctx context.Context, resource core.WorkflowResource, source core.ConfigSource, revision core.WorkflowRevision, qa bool) ([]core.WorkflowCheckReport, error) {
	type target struct{ app, repository, sha string }
	targets := map[string]target{}
	for _, input := range revision.Sources {
		app := source.GitHubAppID
		repo := normalizeRepository(input.Repository)
		for _, pr := range revision.PullRequests {
			if normalizeRepository(pr.Repository) == repo && pr.CommitSHA == input.CommitSHA {
				app = pr.GitHubAppID
				break
			}
		}
		if app != "" && repo != "" && input.CommitSHA != "" {
			targets[app+":"+repo+":"+input.CommitSHA] = target{app, repo, input.CommitSHA}
		}
	}
	if len(targets) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	previewURL := ""
	if revision.Feedback != nil {
		previewURL = revision.Feedback.PreviewURL
	}
	if previewURL == "" && resource.Temporary {
		triggers, err := s.Store.ListWorkflowPreviewTriggers(ctx)
		if err != nil {
			return nil, err
		}
		for _, trigger := range triggers {
			if trigger.ResourceID == resource.ID && trigger.ClosedAt == nil {
				previewURL = trigger.PreviewURL
				break
			}
		}
	}
	type scope struct{ kind, stage, check string }
	kind := "deployment"
	if qa {
		kind = "qa"
	}
	scopes := []scope{{kind: kind}}
	if !qa {
		documents, err := Parse(resource.Path, []byte(resource.Document))
		// Execution validates the document itself. A malformed document still
		// gets its aggregate failure reported without adding a new run gate.
		if err == nil && len(documents) == 1 && documents[0].Spec != nil {
			for _, stage := range documents[0].Spec.Stages {
				for _, name := range sortedCheckNames(stage.Checks) {
					if stage.Checks[name].When != "onDemand" {
						scopes = append(scopes, scope{kind: "health", stage: stage.Name, check: name})
					}
				}
			}
		}
	}
	items := []core.WorkflowCheckReport{}
	for _, key := range keys {
		t := targets[key]
		connection, err := s.Store.GetGitHubApp(ctx, t.app)
		if err != nil {
			return nil, err
		}
		if connection.AppID < 1 || connection.APIURL == "" {
			continue
		}
		for _, scope := range scopes {
			name := "Dispatch/" + scope.kind + "/" + resource.Name
			if scope.stage != "" {
				name += "/" + scope.stage + "/" + scope.check
			}
			if len(name) > 160 {
				digest := sha256.Sum256([]byte(name))
				name = name[:140] + fmt.Sprintf("-%x", digest[:8])
			}
			id := ulid.Make().String()
			items = append(items, core.WorkflowCheckReport{ID: id, RevisionID: revision.ID, ResourceID: resource.ID, ProjectID: source.ProjectID, GitHubAppID: t.app, Repository: t.repository, CommitSHA: t.sha, Name: name, Kind: scope.kind, Stage: scope.stage, Check: scope.check, PreviewURL: previewURL, ExternalID: "dispatch-check:" + id, AppID: connection.AppID, APIURL: strings.TrimRight(connection.APIURL, "/"), State: "pending", UpdatedAt: revision.CreatedAt})
		}
	}
	return items, nil
}
