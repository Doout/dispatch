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
	type scope struct {
		kind, stage, check, job string
		sources                 []string
	}
	kind := "deployment"
	if qa {
		kind = "qa"
	}
	scopes := []scope{{kind: kind}}
	logs := false
	if !qa {
		documents, err := Parse(resource.Path, []byte(resource.Document))
		// Execution validates the document itself. A malformed document still
		// gets its aggregate failure reported without adding a new run gate.
		if err == nil && len(documents) == 1 && documents[0].Spec != nil {
			logs = documents[0].Spec.GitHubLogs
			for _, name := range sortedResourceNames(documents[0].Spec.Jobs) {
				job := documents[0].Spec.Jobs[name]
				scopes = append(scopes, scope{kind: "build", job: name, sources: jobSourceAliases(job)})
			}
			for _, name := range sortedResourceNames(documents[0].Spec.Finally) {
				job := documents[0].Spec.Finally[name]
				scopes = append(scopes, scope{kind: "build", job: "finally/" + name, sources: jobSourceAliases(job)})
			}
			for _, stage := range documents[0].Spec.Stages {
				scopes = append(scopes, scope{kind: "stage", stage: stage.Name})
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
			// A build belongs on the commits it consumes, including additional
			// sources when its build script runs from a configuration repository.
			if len(scope.sources) > 0 {
				matched := false
				for _, alias := range scope.sources {
					input := revision.Sources[alias]
					if normalizeRepository(input.Repository) == t.repository && input.CommitSHA == t.sha {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
			name := "Dispatch/" + scope.kind + "/" + resource.Name
			if scope.stage != "" {
				name += "/" + scope.stage
				if scope.check != "" {
					name += "/" + scope.check
				}
			}
			if scope.job != "" {
				name += "/" + scope.job
			}
			if len(name) > 160 {
				digest := sha256.Sum256([]byte(name))
				name = name[:140] + fmt.Sprintf("-%x", digest[:8])
			}
			id := ulid.Make().String()
			items = append(items, core.WorkflowCheckReport{ID: id, RevisionID: revision.ID, ResourceID: resource.ID, ProjectID: source.ProjectID, GitHubAppID: t.app, Repository: t.repository, CommitSHA: t.sha, Name: name, Kind: scope.kind, Stage: scope.stage, Check: scope.check, Job: scope.job, PreviewURL: previewURL, ExternalID: "dispatch-check:" + id, AppID: connection.AppID, APIURL: strings.TrimRight(connection.APIURL, "/"), State: "pending", UpdatedAt: revision.CreatedAt})
			if scope.kind == "deployment" && logs {
				// Resolve the collector branch in the reporting queue. GitHub
				// availability must not gate acceptance or execution of a run.
				items[len(items)-1].Logs = &core.GitHubLogWorkflow{}
			}
		}
	}
	return items, nil
}
