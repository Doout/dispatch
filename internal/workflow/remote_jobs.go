package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflowrunner"
)

type workerSource = workflowrunner.Source

func (s *Service) AuthorizeWorkerRequest(ctx context.Context, request workflowrunner.Request) error {
	if request.Workflow == nil {
		return errors.New("workflow request is required")
	}
	if request.Workflow.ServiceRunID != "" {
		return s.authorizeWorkerService(ctx, request)
	}
	revision, err := s.Store.GetWorkflowRevision(ctx, request.RevisionID)
	if err != nil {
		return err
	}
	if revision.ResourceID != request.ResourceID || revision.State != "running" {
		return errors.New("workflow revision is no longer running")
	}
	resource, err := s.Store.GetWorkflowResource(ctx, request.ResourceID)
	if err != nil {
		return err
	}
	source, err := s.Store.GetConfigSource(ctx, resource.ConfigSourceID)
	if err != nil {
		return err
	}
	if source.ProjectID != request.ProjectID {
		return errors.New("workflow project ownership changed")
	}
	return s.checkRevisionTrust(ctx, revision)
}
func (r *jobRuntime) runRemoteJob(ctx context.Context, job JobSpec, secrets map[string]string, progress func(string)) (map[string]string, string, error) {
	if r.service.RemoteJobs == nil {
		return nil, "", errors.New("this controller requires an enrolled workflow worker")
	}
	if err := r.checkExecutionTrust(ctx); err != nil {
		return nil, "", err
	}
	sources := map[string]workerSource{}
	for _, alias := range jobSourceAliases(job) {
		revision, ok := r.revision.Sources[alias]
		if !ok {
			return nil, "", fmt.Errorf("source %s is missing from the revision", alias)
		}
		source, err := r.service.workerSource(ctx, r.source, revision)
		if err != nil {
			return nil, "", err
		}
		sources[alias] = source
	}
	raw, err := json.Marshal(job)
	if err != nil {
		return nil, "", err
	}
	mode := job.WorkerMode
	if mode == "" {
		mode = r.service.WorkerMode
	}
	if mode == "" {
		mode = "tenant"
	}
	request := workflowrunner.Request{Version: workflowrunner.Version, ProjectID: r.source.ProjectID, ResourceID: r.revision.ResourceID, RevisionID: r.revision.ID, Mode: mode, Workflow: &workflowrunner.Workflow{Job: raw, Sources: sources, Inputs: r.inputs, Secrets: secrets}}
	if r.serviceTemplate != nil {
		request.Workflow.ServiceRunID = r.serviceRunID
		digest := sha256.Sum256([]byte(r.serviceTemplate.Document))
		request.Workflow.TemplateDigest = hex.EncodeToString(digest[:])
		if parent, ok := ctx.Value(previewTrustParentKey{}).(core.WorkflowRevision); ok {
			request.Workflow.ParentRevisionID = parent.ID
		}
	}
	result, err := r.service.RemoteJobs.Run(ctx, request, progress)
	return result.Outputs, result.Log, err
}
func (s *Service) workerSource(ctx context.Context, source core.ConfigSource, revision core.WorkflowSourceRevision) (workerSource, error) {
	url, err := s.repositoryCloneURL(ctx, source, revision.Repository)
	if err != nil {
		return workerSource{}, err
	}
	if s.RequireRemote && !remoteWorkerSourceURL(url) {
		return workerSource{}, errors.New("hosted repositories must use HTTPS or SSH")
	}
	item := workerSource{Revision: revision, URL: url}
	if source.GitHubAppID != "" {
		if s.GitHub == nil {
			return item, errors.New("GitHub App access is not configured")
		}
		item.AuthType = deploy.SourceAuthGitHubApp
		item.Credential, err = s.GitHub.RepositoryToken(ctx, source.GitHubAppID, revision.Repository)
		return item, err
	}
	if source.CredentialSecretID == "" || s.Secrets == nil {
		return item, errors.New("repository credential resolution is not configured")
	}
	secret, err := s.Store.GetSecret(ctx, source.CredentialSecretID)
	if err != nil {
		return item, err
	}
	credential, err := s.Secrets.Resolve(ctx, secret.ID)
	if err != nil {
		return item, err
	}
	defer clear(credential)
	item.AuthType = deploy.SourceAuthGitHubToken
	if secret.Type == core.SecretTypeSSHPrivateKey {
		item.AuthType = deploy.SourceAuthSSHKey
	}
	if err = deploy.ValidateSourceCredentialType(item.AuthType, secret.Type); err != nil {
		return item, err
	}
	item.Credential = string(credential)
	return item, nil
}

// ExecuteWorkerJob runs only inside the separately configured worker process.
// The caller owns cancellation, isolation and the private workspace lifecycle.
func ExecuteWorkerJob(ctx context.Context, request workflowrunner.Request, workspace string, progress func(string)) workflowrunner.Result {
	failure := func(err error) workflowrunner.Result {
		state := "failed"
		if ctx.Err() != nil {
			state = "cancelled"
		}
		return workflowrunner.Result{State: state, Error: request.Redact(err.Error())}
	}
	if err := request.Validate(); err != nil {
		return failure(err)
	}
	if request.Workflow == nil {
		return failure(errors.New("workflow payload is required"))
	}
	var job JobSpec
	if err := json.Unmarshal(request.Workflow.Job, &job); err != nil {
		return failure(err)
	}
	if job.Builder != "" && job.Builder != "docker" {
		return failure(errors.New("unsupported job builder"))
	}
	if request.Mode == "managed" && job.Builder != "" {
		return failure(errors.New("managed jobs cannot use a Docker builder"))
	}
	// A tenant worker uses its own configured Docker daemon. Controller SSH builder
	// selection must never run inside the worker or fall back to controller Docker.
	job.Builder = ""
	info, err := os.Lstat(workspace)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return failure(errors.New("worker workspace must be a private directory"))
	}
	revision := core.WorkflowRevision{ID: request.RevisionID, ResourceID: request.ResourceID, Sources: map[string]core.WorkflowSourceRevision{}}
	for alias, source := range request.Workflow.Sources {
		revision.Sources[alias] = source.Revision
	}
	runtime := jobRuntime{root: workspace, revision: revision, paths: map[string]string{}, inputs: request.Workflow.Inputs, workerSources: request.Workflow.Sources}
	defer runtime.close()
	output, logs, err := runtime.runJobCommand(ctx, job, request.Workflow.Secrets, func(log string) {
		if progress != nil {
			progress(request.Redact(log))
		}
	})
	result := workflowrunner.Result{State: "succeeded", Log: request.Redact(logs), Outputs: output}
	if err != nil {
		failed := failure(err)
		result.State, result.Error = failed.State, failed.Error
	}
	return result
}
func (r *jobRuntime) checkoutWorkerSource(ctx context.Context, alias string) (string, error) {
	if path := r.paths[alias]; path != "" {
		return path, nil
	}
	source, ok := r.workerSources[alias]
	if !ok {
		return "", fmt.Errorf("unknown source %s", alias)
	}
	// The worker accepts remote repositories only; file:// and local paths must
	// never turn controller-supplied metadata into access to worker host files.
	url := strings.TrimSpace(source.URL)
	if !remoteWorkerSourceURL(url) {
		return "", errors.New("worker source must use HTTPS or SSH")
	}
	if !commitRefPattern.MatchString(source.Revision.CommitSHA) {
		return "", errors.New("worker source requires an immutable commit")
	}
	environment, cleanup, err := deploy.PrepareGitEnvironment(core.App{SourceAuthType: source.AuthType, SourceCredential: source.Credential})
	if err != nil {
		return "", err
	}
	defer cleanup()
	environment = append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	root := filepath.Join(r.root, "sources", safePathPart(alias))
	cache := newRepositoryCache(filepath.Join(r.root, "repositories"))
	tree, err := cache.checkout(ctx, url, "job", source.Revision.Branch, source.Revision.CommitSHA, root, environment)
	if err != nil {
		return "", err
	}
	r.worktrees = append(r.worktrees, tree)
	path := root
	if source.Revision.Path != "" {
		path, err = safeJoin(root, source.Revision.Path)
		if err != nil {
			return "", err
		}
	}
	if _, err = containedPath(root, path); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", errors.New("worker source path must be a directory")
	}
	r.paths[alias] = path
	return path, nil
}

func remoteWorkerSourceURL(value string) bool { return workflowrunner.ValidRepositoryURL(value) }

func (s *Service) authorizeWorkerService(ctx context.Context, request workflowrunner.Request) error {
	run, err := s.Store.GetServiceProvisionRun(ctx, request.Workflow.ServiceRunID)
	if err != nil {
		return err
	}
	if run.ProjectID != request.ProjectID || run.TemplateID != request.ResourceID || run.State != "running" {
		return errors.New("service provision ownership changed")
	}
	document := ""
	resource, err := s.Store.GetWorkflowResource(ctx, run.TemplateID)
	if err == nil {
		source, sourceErr := s.Store.GetConfigSource(ctx, resource.ConfigSourceID)
		if sourceErr != nil || source.ProjectID != run.ProjectID || !resource.Active || resource.Kind != KindServiceTemplate {
			return errors.New("service template is unavailable in this project")
		}
		document = resource.Document
	} else if errors.Is(err, store.ErrNotFound) {
		saved, savedErr := s.Store.GetSavedServiceTemplate(ctx, run.TemplateID)
		if savedErr != nil || saved.ProjectID != run.ProjectID {
			return errors.New("saved service template is unavailable in this project")
		}
		document = saved.Document
	} else {
		return err
	}
	digest := sha256.Sum256([]byte(document))
	if hex.EncodeToString(digest[:]) != request.Workflow.TemplateDigest {
		return errors.New("service template changed before execution")
	}
	if parentID := request.Workflow.ParentRevisionID; parentID != "" {
		parent, err := s.Store.GetWorkflowRevision(ctx, parentID)
		if err != nil {
			return err
		}
		resource, err := s.Store.GetWorkflowResource(ctx, parent.ResourceID)
		if err != nil {
			return err
		}
		source, err := s.Store.GetConfigSource(ctx, resource.ConfigSourceID)
		if err != nil || source.ProjectID != run.ProjectID {
			return errors.New("service parent belongs to another project")
		}
		return s.checkRevisionTrust(ctx, parent)
	}
	return nil
}
