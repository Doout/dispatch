package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
	"gopkg.in/yaml.v3"
)

func (s *Service) runStages(ctx context.Context, resource core.WorkflowResource, source core.ConfigSource, document Document, revision *core.WorkflowRevision, start int) error {
	if len(document.Spec.Stages) == 0 {
		s.succeedRevision(ctx, source, revision)
		return nil
	}
	for index := start; index < len(document.Spec.Stages); index++ {
		stage := document.Spec.Stages[index]
		run, exists, err := s.stageRun(ctx, revision.ID, stage.Name)
		if err != nil {
			return err
		}
		if !exists {
			state := "queued"
			if stage.Approval == "required" {
				state = "awaiting_approval"
			}
			run = core.WorkflowStageRun{ID: ulid.Make().String(), RevisionID: revision.ID, StageName: stage.Name, TargetRef: stage.TargetRef,
				State: state, Approval: stage.Approval, DeploymentIDs: []string{}, CheckRuns: map[string]string{}, CreatedAt: time.Now().UTC()}
			if err := s.Store.CreateWorkflowStageRun(ctx, run); err != nil {
				return err
			}
		}
		if run.State == "awaiting_approval" {
			revision.State = "awaiting_approval"
			return s.Store.UpdateWorkflowRevision(ctx, *revision)
		}
		now := time.Now().UTC()
		run.State, run.StartedAt = "running", &now
		if err := s.Store.UpdateWorkflowStageRun(ctx, run); err != nil {
			return err
		}
		if err := s.deployStage(ctx, resource, source, document, *revision, stage, &run); err != nil {
			finish := time.Now().UTC()
			run.State, run.Error, run.FinishedAt = "failed", err.Error(), &finish
			_ = s.Store.UpdateWorkflowStageRun(context.Background(), run)
			return fmt.Errorf("stage %s: %w", stage.Name, err)
		}
		finish := time.Now().UTC()
		run.State, run.FinishedAt = "succeeded", &finish
		if err := s.Store.UpdateWorkflowStageRun(ctx, run); err != nil {
			return err
		}
	}
	s.succeedRevision(ctx, source, revision)
	return nil
}

func (s *Service) stageRun(ctx context.Context, revisionID, name string) (core.WorkflowStageRun, bool, error) {
	runs, err := s.Store.ListWorkflowStageRuns(ctx, revisionID)
	if err != nil {
		return core.WorkflowStageRun{}, false, err
	}
	for _, run := range runs {
		if run.StageName == name {
			return run, true, nil
		}
	}
	return core.WorkflowStageRun{}, false, nil
}

func (s *Service) deployStage(ctx context.Context, resource core.WorkflowResource, source core.ConfigSource, document Document, revision core.WorkflowRevision, stage StageSpec, run *core.WorkflowStageRun) error {
	server, err := s.resolveTarget(ctx, stage.TargetRef)
	if err != nil {
		return err
	}
	for _, name := range stage.Deploy {
		deployment := document.Spec.Deployments[name]
		id, err := s.deployHelm(ctx, resource, source, revision, stage, name, deployment, server)
		if err != nil {
			return err
		}
		run.DeploymentIDs = append(run.DeploymentIDs, id)
		if err := s.Store.UpdateWorkflowStageRun(ctx, *run); err != nil {
			return err
		}
	}
	for _, name := range sortedCheckNames(stage.Checks) {
		check := stage.Checks[name]
		inputs := map[string]string{}
		for key, value := range check.With {
			rendered, err := renderRuntime(value, nil, revision.Sources, nil, &stage)
			if err != nil {
				return err
			}
			inputs[key] = rendered
		}
		checkRevision, err := s.runPipeline(ctx, check.PipelineRef, inputs, resource.Name+"/"+stage.Name)
		if err != nil {
			return fmt.Errorf("check %s: %w", name, err)
		}
		run.CheckRuns[name] = checkRevision.ID
		if err := s.Store.UpdateWorkflowStageRun(ctx, *run); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) resolveTarget(ctx context.Context, ref string) (core.Server, error) {
	if item, err := s.Store.GetServer(ctx, ref); err == nil {
		if item.State != "ready" {
			return item, fmt.Errorf("target %s is not ready", ref)
		}
		return item, nil
	}
	items, err := s.Store.ListServers(ctx)
	if err != nil {
		return core.Server{}, err
	}
	for _, item := range items {
		if strings.EqualFold(item.Name, ref) {
			if item.State != "ready" {
				return item, fmt.Errorf("target %s is not ready", ref)
			}
			return item, nil
		}
	}
	return core.Server{}, fmt.Errorf("target %s was not found", ref)
}

func (s *Service) deployHelm(ctx context.Context, resource core.WorkflowResource, source core.ConfigSource, revision core.WorkflowRevision, stage StageSpec, deploymentName string, spec DeploymentSpec, server core.Server) (string, error) {
	chart, ok := revision.Sources[spec.Helm.SourceRef]
	if !ok {
		return "", fmt.Errorf("chart source %s is missing", spec.Helm.SourceRef)
	}
	values, err := renderValues(spec.Helm.Values, revision.Sources, stage)
	if err != nil {
		return "", err
	}
	for path, binding := range spec.Helm.Bindings {
		jobName, outputName, _ := strings.Cut(binding.OutputRef, ".")
		value, ok := revision.Outputs[jobName][outputName]
		if !ok {
			return "", fmt.Errorf("output %s is unavailable", binding.OutputRef)
		}
		if err := setNestedValue(values, path, value); err != nil {
			return "", err
		}
	}
	valuesYAML, err := yaml.Marshal(values)
	if err != nil {
		return "", err
	}
	repositoryURL, err := s.repositoryCloneURL(ctx, source, chart.Repository)
	if err != nil {
		return "", err
	}
	appID := managedAppID(resource.ID, deploymentName, stage.Name)
	app, err := s.Store.GetApp(ctx, appID)
	if errors.Is(err, store.ErrNotFound) {
		app = core.App{ID: appID, ProjectID: source.ProjectID, CreatedAt: time.Now().UTC(), Generated: true}
	} else if err != nil {
		return "", err
	}
	app.ServerID, app.Name, app.SourceRepo, app.Branch = server.ID, "managed-"+resource.Name+"-"+deploymentName+"-"+stage.Name, repositoryURL, chart.Branch
	if source.GitHubAppID != "" {
		app.SourceAuthType, app.SourceCredentialID = deploy.SourceAuthGitHubApp, source.GitHubAppID
	} else {
		secret, err := s.Store.GetSecret(ctx, source.CredentialSecretID)
		if err != nil {
			return "", err
		}
		app.SourceAuthType, app.SourceCredentialID = deploy.SourceAuthGitHubToken, source.CredentialSecretID
		if secret.Type == core.SecretTypeSSHPrivateKey {
			app.SourceAuthType = deploy.SourceAuthSSHKey
		}
	}
	chartPath := chart.Path
	if chartPath == "" {
		chartPath = "."
	}
	app.BuildType, app.HelmChart, app.HelmValues, app.State = core.BuildTypeHelm, chartPath, string(valuesYAML), "ready"
	app.HelmNamespace, err = renderRuntime(spec.Helm.Namespace, nil, revision.Sources, nil, &stage)
	if err != nil {
		return "", err
	}
	app.HelmRelease, err = renderRuntime(spec.Helm.ReleaseName, nil, revision.Sources, nil, &stage)
	if err != nil {
		return "", err
	}
	app.Domain = stage.URL
	if _, getErr := s.Store.GetApp(ctx, appID); errors.Is(getErr, store.ErrNotFound) {
		err = s.Store.CreateApp(ctx, app)
	} else {
		err = s.Store.UpdateApp(ctx, app)
	}
	if err != nil {
		return "", err
	}
	deployment, err := s.Deployments.Start(ctx, app.ID, chart.CommitSHA)
	if err != nil {
		return "", err
	}
	return deployment.ID, s.waitDeployment(ctx, deployment.ID)
}

func (s *Service) waitDeployment(ctx context.Context, id string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		deployment, err := s.Store.GetDeployment(ctx, id)
		if err != nil {
			return err
		}
		if deployment.State.Terminal() {
			if deployment.State != core.DeploymentSucceeded {
				return errors.New(deployment.Message)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) runPipeline(ctx context.Context, name string, inputs map[string]string, trigger string) (core.WorkflowRevision, error) {
	resources, err := s.Store.ListWorkflowResources(ctx, "")
	if err != nil {
		return core.WorkflowRevision{}, err
	}
	var resource core.WorkflowResource
	for _, candidate := range resources {
		if candidate.Active && candidate.Kind == KindPipeline && candidate.Name == name {
			if resource.ID != "" {
				return core.WorkflowRevision{}, fmt.Errorf("pipeline %s is ambiguous", name)
			}
			resource = candidate
		}
	}
	if resource.ID == "" {
		return core.WorkflowRevision{}, fmt.Errorf("pipeline %s was not found or is paused", name)
	}
	source, err := s.Store.GetConfigSource(ctx, resource.ConfigSourceID)
	if err != nil {
		return core.WorkflowRevision{}, err
	}
	documents, err := Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].Pipeline == nil {
		return core.WorkflowRevision{}, errors.New("stored pipeline document is invalid")
	}
	for key, spec := range documents[0].Pipeline.Inputs {
		if _, ok := inputs[key]; !ok && spec.Default != nil {
			inputs[key] = fmt.Sprint(spec.Default)
		}
		if spec.Required && strings.TrimSpace(inputs[key]) == "" {
			return core.WorkflowRevision{}, fmt.Errorf("pipeline input %s is required", key)
		}
	}
	snapshot := map[string]core.WorkflowSourceRevision{}
	for alias, spec := range documents[0].Pipeline.Sources {
		sha, err := s.repositoryHead(ctx, source, spec.Repository, spec.Branch)
		if err != nil {
			return core.WorkflowRevision{}, err
		}
		snapshot[alias] = core.WorkflowSourceRevision{Alias: alias, Repository: spec.Repository, Branch: spec.Branch, CommitSHA: sha, Path: spec.Path}
	}
	started := time.Now().UTC()
	revision := core.WorkflowRevision{ID: ulid.Make().String(), ResourceID: resource.ID, ConfigSHA: resource.ConfigSHA, SpecDigest: resource.SpecDigest,
		State: "running", Trigger: trigger, Sources: snapshot, Outputs: map[string]map[string]string{}, CreatedAt: started, StartedAt: &started}
	if err := s.Store.CreateWorkflowRevision(ctx, revision); err != nil {
		return revision, err
	}
	root, err := os.MkdirTemp("", "dispatch-pipeline-")
	if err != nil {
		s.failRevision(ctx, source, &revision, err)
		return revision, err
	}
	defer os.RemoveAll(root)
	runtime := &jobRuntime{service: s, source: source, revision: revision, root: root, paths: map[string]string{}, inputs: inputs}
	defer runtime.close()
	revision.Outputs, err = runtime.executeJobs(ctx, resource, documents[0].Pipeline.Jobs, documents[0].Pipeline.Finally, true)
	if err != nil {
		s.failRevision(ctx, source, &revision, err)
		return revision, err
	}
	finished := time.Now().UTC()
	revision.State, revision.FinishedAt = "succeeded", &finished
	err = s.Store.UpdateWorkflowRevision(ctx, revision)
	return revision, err
}

func (s *Service) ApproveStage(ctx context.Context, id string) (core.WorkflowStageRun, error) {
	run, err := s.Store.GetWorkflowStageRun(ctx, id)
	if err != nil {
		return run, err
	}
	if run.State != "awaiting_approval" {
		return run, errors.New("stage is not awaiting approval")
	}
	revision, err := s.Store.GetWorkflowRevision(ctx, run.RevisionID)
	if err != nil {
		return run, err
	}
	resource, err := s.Store.GetWorkflowResource(ctx, revision.ResourceID)
	if err != nil {
		return run, err
	}
	source, err := s.Store.GetConfigSource(ctx, resource.ConfigSourceID)
	if err != nil {
		return run, err
	}
	documents, err := Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].Spec == nil {
		return run, errors.New("stored application document is invalid")
	}
	index := -1
	for current, stage := range documents[0].Spec.Stages {
		if stage.Name == run.StageName {
			index = current
			break
		}
	}
	if index < 0 {
		return run, errors.New("stage is no longer present in the application")
	}
	run.State, run.Approval = "queued", "approved"
	if err := s.Store.UpdateWorkflowStageRun(ctx, run); err != nil {
		return run, err
	}
	revision.State = "running"
	if err := s.Store.UpdateWorkflowRevision(ctx, revision); err != nil {
		return run, err
	}
	go func() {
		if err := s.runStages(context.Background(), resource, source, documents[0], &revision, index); err != nil {
			s.failRevision(context.Background(), source, &revision, err)
		}
	}()
	return run, nil
}

func renderValues(input map[string]any, sources map[string]core.WorkflowSourceRevision, stage StageSpec) (map[string]any, error) {
	result := map[string]any{}
	for key, value := range input {
		rendered, err := renderValue(value, sources, stage)
		if err != nil {
			return nil, err
		}
		result[key] = rendered
	}
	return result, nil
}

func renderValue(value any, sources map[string]core.WorkflowSourceRevision, stage StageSpec) (any, error) {
	switch current := value.(type) {
	case string:
		return renderRuntime(current, nil, sources, nil, &stage)
	case map[string]any:
		return renderValues(current, sources, stage)
	case []any:
		result := make([]any, len(current))
		for index, item := range current {
			rendered, err := renderValue(item, sources, stage)
			if err != nil {
				return nil, err
			}
			result[index] = rendered
		}
		return result, nil
	default:
		return value, nil
	}
}

func setNestedValue(values map[string]any, path string, value any) error {
	parts := strings.Split(path, ".")
	current := values
	for index, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return fmt.Errorf("Helm value path %s is invalid", path)
		}
		if index == len(parts)-1 {
			current[part] = value
			return nil
		}
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	return nil
}

func sortedCheckNames(checks map[string]CheckSpec) []string {
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func managedAppID(resourceID, deployment, stage string) string {
	digest := sha256.Sum256([]byte(resourceID + "\x00" + deployment + "\x00" + stage))
	return "workflow-" + hex.EncodeToString(digest[:12])
}
