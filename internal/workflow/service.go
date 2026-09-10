package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/secretvalue"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

const defaultPollInterval = 5 * 60

type Service struct {
	Store        store.Store
	GitHub       *githubapp.Manager
	Secrets      *secretvalue.Resolver
	Deployments  *deploy.Service
	Logger       *slog.Logger
	Repositories *repositoryCache

	mu         sync.Mutex
	locks      map[string]*sync.Mutex
	buildLocks map[string]*buildLock
}

type Push struct {
	Repository string
	Branch     string
	CommitSHA  string
	Deleted    bool
}

type parsedResource struct {
	path     string
	document Document
}

func NewService(data store.Store, github *githubapp.Manager, secrets *secretvalue.Resolver, deployments *deploy.Service, logger *slog.Logger, cacheRoots ...string) *Service {
	cacheRoot := ""
	if len(cacheRoots) > 0 {
		cacheRoot = cacheRoots[0]
	}
	return &Service{Store: data, GitHub: github, Secrets: secrets, Deployments: deployments, Logger: logger,
		Repositories: newRepositoryCache(cacheRoot), locks: map[string]*sync.Mutex{}}
}

func (s *Service) SyncSource(ctx context.Context, id string) (core.ConfigSource, error) {
	unlock := s.lock("source:" + id)
	defer unlock()
	source, err := s.Store.GetConfigSource(ctx, id)
	if err != nil {
		return source, err
	}
	if source.GitHubAppID == "" && source.CredentialSecretID == "" {
		return s.sourceError(ctx, source, errors.New("repository access is not configured"))
	}
	webhookErr := error(nil)
	if source.SyncMode != core.ConfigSyncPoll {
		webhookErr = s.GitHub.EnsureWebhookConfig(ctx, source.GitHubAppID)
		if webhookErr == nil {
			verification, verifyErr := s.GitHub.Verify(ctx, source.GitHubAppID)
			if verifyErr != nil {
				webhookErr = verifyErr
			} else if !verification.PushSubscribed {
				webhookErr = errors.New("the GitHub App is not subscribed to push events")
			}
		}
	}
	head, err := s.repositoryHead(ctx, source, source.Repository, source.Branch)
	if err != nil {
		return s.sourceError(ctx, source, err)
	}
	files, err := s.repositoryFiles(ctx, source, head)
	if err != nil {
		return s.sourceError(ctx, source, err)
	}
	parsed, err := s.parseConfiguration(ctx, source, head, files)
	if err != nil {
		return s.sourceError(ctx, source, err)
	}
	validatedSources := map[string]bool{}
	for _, value := range parsed {
		sources := map[string]SourceSpec{}
		if value.document.Spec != nil {
			sources = value.document.Spec.Sources
		} else if value.document.Pipeline != nil {
			sources = value.document.Pipeline.Sources
		}
		for alias, spec := range sources {
			key := normalizeRepository(spec.Repository) + "@" + sourceRevisionRef(spec)
			if validatedSources[key] {
				continue
			}
			if _, err := s.repositoryHead(ctx, source, spec.Repository, sourceRevisionRef(spec)); err != nil {
				return s.sourceError(ctx, source, fmt.Errorf("validate source %s in %s: %w", alias, value.path, err))
			}
			validatedSources[key] = true
		}
	}
	existing, err := s.Store.ListWorkflowResources(ctx, source.ID)
	if err != nil {
		return s.sourceError(ctx, source, err)
	}
	now := time.Now().UTC()
	desired := make([]core.WorkflowResource, 0, len(parsed))
	for _, value := range parsed {
		digest, err := value.document.Digest()
		if err != nil {
			return s.sourceError(ctx, source, err)
		}
		contents, err := value.document.MarshalYAML()
		if err != nil {
			return s.sourceError(ctx, source, err)
		}
		item, found, err := existingApplication(existing, value.path, value.document.Kind, value.document.Metadata.Name)
		if err != nil {
			return s.sourceError(ctx, source, err)
		}
		if !found {
			item = core.WorkflowResource{ID: ulid.Make().String(), ConfigSourceID: source.ID, CreatedAt: now, Active: true, State: "ready"}
		} else if item.Active {
			item.State = "ready"
		} else if item.State != "paused" {
			item.State = "pending"
		}
		item.APIVersion, item.Kind, item.Name, item.Path = value.document.APIVersion, value.document.Kind, value.document.Metadata.Name, value.path
		item.Document, item.SpecDigest, item.ConfigSHA = string(contents), digest, head
		item.LastError, item.UpdatedAt = "", now
		desired = append(desired, item)
	}
	source.LastSeenSHA, source.LastSyncedAt, source.UpdatedAt = head, &now, now
	source.State, source.LastError = "ready", ""
	if webhookErr != nil {
		if source.SyncMode == core.ConfigSyncWebhook {
			source.State, source.LastError = "invalid", webhookErr.Error()
		} else {
			source.State, source.LastError = "degraded", "Polling is active. "+webhookErr.Error()
		}
	}
	if err := s.Store.ReplaceWorkflowResources(ctx, source, desired); err != nil {
		return source, err
	}
	if webhookErr != nil && source.SyncMode == core.ConfigSyncWebhook {
		return source, webhookErr
	}
	if source.Active {
		for _, resource := range desired {
			if !resource.Active || resource.Kind != KindApplication {
				continue
			}
			snapshot, err := s.resolveResourceSources(ctx, source, resource)
			if err == nil && s.snapshotChanged(ctx, resource, snapshot) {
				_, err = s.startWithSnapshot(ctx, resource, source, snapshot, "configuration sync")
			}
			if err != nil {
				return s.sourceError(ctx, source, fmt.Errorf("start application %s: %w", resource.Name, err))
			}
		}
	}
	return source, nil
}

func validateResourceSet(resources []parsedResource) error {
	seen := map[string]string{}
	for _, value := range resources {
		key := value.document.Kind + "/" + value.document.Metadata.Name
		if previous, ok := seen[key]; ok {
			return fmt.Errorf("%s and %s both define %s", previous, value.path, key)
		}
		seen[key] = value.path
	}
	return nil
}

func (s *Service) sourceError(ctx context.Context, source core.ConfigSource, cause error) (core.ConfigSource, error) {
	now := time.Now().UTC()
	source.State, source.LastError, source.UpdatedAt = "invalid", cause.Error(), now
	if err := s.Store.UpdateConfigSource(ctx, source); err != nil {
		return source, errors.Join(cause, err)
	}
	return source, cause
}

func ParsePush(contents []byte) (Push, error) {
	var payload struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Deleted    bool   `json:"deleted"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(contents, &payload); err != nil {
		return Push{}, fmt.Errorf("decode push event: %w", err)
	}
	branch, ok := strings.CutPrefix(payload.Ref, "refs/heads/")
	if !ok || strings.TrimSpace(payload.Repository.FullName) == "" || strings.TrimSpace(branch) == "" {
		return Push{}, errors.New("push event is missing its repository or branch")
	}
	return Push{Repository: normalizeRepository(payload.Repository.FullName), Branch: branch, CommitSHA: payload.After, Deleted: payload.Deleted}, nil
}

// HandlePush persists one deduplicated event for each configuration source
// whose active resources reference the pushed branch.
func (s *Service) HandlePush(ctx context.Context, connectionID, deliveryID string, contents []byte) (int, error) {
	push, err := ParsePush(contents)
	if err != nil || push.Deleted {
		return 0, err
	}
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		digest := sha256.Sum256(contents)
		deliveryID = "body:" + hex.EncodeToString(digest[:])
	}
	sources, err := s.Store.ListConfigSources(ctx)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, source := range sources {
		if !source.Active || source.GitHubAppID != connectionID || source.SyncMode == core.ConfigSyncPoll {
			continue
		}
		matches := sameSource(push.Repository, push.Branch, source.Repository, source.Branch)
		if !matches {
			resources, listErr := s.Store.ListWorkflowResources(ctx, source.ID)
			if listErr != nil {
				return created, listErr
			}
			for _, resource := range resources {
				if resource.Active && resourceReferences(resource, push.Repository, push.Branch) {
					matches = true
					break
				}
			}
		}
		if !matches {
			continue
		}
		event := core.WorkflowEvent{ID: ulid.Make().String(), ConfigSourceID: source.ID, Provider: "github", DeliveryID: deliveryID,
			Kind: "push", Repository: push.Repository, Branch: push.Branch, CommitSHA: push.CommitSHA, State: "queued", CreatedAt: time.Now().UTC()}
		inserted, createErr := s.Store.CreateWorkflowEvent(ctx, event)
		if createErr != nil {
			return created, createErr
		}
		if inserted {
			created++
			go s.processEvent(context.Background(), event)
		}
	}
	return created, nil
}

func (s *Service) processEvent(ctx context.Context, event core.WorkflowEvent) {
	event.State = "running"
	_ = s.Store.UpdateWorkflowEvent(ctx, event)
	source, err := s.Store.GetConfigSource(ctx, event.ConfigSourceID)
	if err == nil && sameSource(event.Repository, event.Branch, source.Repository, source.Branch) {
		_, err = s.SyncSource(ctx, source.ID)
	}
	if err == nil {
		resources, listErr := s.Store.ListWorkflowResources(ctx, source.ID)
		err = listErr
		for _, resource := range resources {
			if err != nil || !resource.Active || resource.Kind != KindApplication {
				continue
			}
			configChanged := sameSource(event.Repository, event.Branch, source.Repository, source.Branch)
			if configChanged || resourceReferences(resource, event.Repository, event.Branch) {
				snapshot, runErr := s.resolveResourceSources(ctx, source, resource)
				if runErr == nil && s.snapshotChanged(ctx, resource, snapshot) {
					_, runErr = s.startWithSnapshot(ctx, resource, source, snapshot, "github push")
				}
				if runErr != nil {
					err = runErr
				}
			}
		}
	}
	now := time.Now().UTC()
	event.ProcessedAt = &now
	if err != nil {
		event.State, event.Error = "failed", err.Error()
	} else {
		event.State = "processed"
	}
	_ = s.Store.UpdateWorkflowEvent(ctx, event)
}

func resourceReferences(resource core.WorkflowResource, repository, branch string) bool {
	documents, err := Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].Spec == nil {
		return false
	}
	for _, source := range documents[0].Spec.Sources {
		if sameSource(repository, branch, source.Repository, strings.TrimPrefix(sourceRevisionRef(source), "refs/heads/")) {
			return true
		}
	}
	return false
}

func sameSource(repository, branch, otherRepository, otherBranch string) bool {
	return normalizeRepository(repository) == normalizeRepository(otherRepository) && strings.EqualFold(strings.TrimSpace(branch), strings.TrimSpace(otherBranch))
}

func normalizeRepository(repository string) string {
	repository = strings.TrimSpace(repository)
	if marker := strings.Index(repository, "://"); marker >= 0 {
		parts := strings.SplitN(repository[marker+3:], "/", 2)
		if len(parts) == 2 {
			repository = parts[1]
		}
	}
	repository = strings.TrimSuffix(strings.Trim(repository, "/"), ".git")
	return strings.ToLower(repository)
}

func (s *Service) RunPoller(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	_ = s.PollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.PollOnce(ctx); err != nil && s.Logger != nil {
				s.Logger.Error("workflow poll failed", "error", err)
			}
		}
	}
}

func (s *Service) PollOnce(ctx context.Context) error {
	sources, err := s.Store.ListConfigSources(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var joined error
	for _, source := range sources {
		if !source.Active || source.SyncMode == core.ConfigSyncWebhook || !pollDue(source, now) {
			continue
		}
		source.LastPolledAt, source.UpdatedAt = &now, now
		if err := s.Store.UpdateConfigSource(ctx, source); err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		head, err := s.repositoryHead(ctx, source, source.Repository, source.Branch)
		if err != nil {
			_, recordErr := s.sourceError(ctx, source, err)
			joined = errors.Join(joined, recordErr)
			continue
		}
		configChanged := head != source.LastSeenSHA
		if configChanged {
			if _, err := s.SyncSource(ctx, source.ID); err != nil {
				joined = errors.Join(joined, err)
				continue
			}
		}
		resources, err := s.Store.ListWorkflowResources(ctx, source.ID)
		if err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		for _, resource := range resources {
			if !resource.Active || resource.Kind != KindApplication {
				continue
			}
			snapshot, err := s.resolveResourceSources(ctx, source, resource)
			if err != nil {
				joined = errors.Join(joined, err)
				continue
			}
			if !s.snapshotChanged(ctx, resource, snapshot) {
				continue
			}
			delivery := "poll:" + resource.ID + ":" + resource.SpecDigest + ":" + snapshotDigest(snapshot)
			event := core.WorkflowEvent{ID: ulid.Make().String(), ConfigSourceID: source.ID, Provider: "poll", DeliveryID: delivery,
				Kind: "branch_scan", Repository: source.Repository, Branch: source.Branch, CommitSHA: head, State: "running", CreatedAt: now}
			inserted, err := s.Store.CreateWorkflowEvent(ctx, event)
			if err != nil || !inserted {
				joined = errors.Join(joined, err)
				continue
			}
			if _, err := s.startWithSnapshot(ctx, resource, source, snapshot, "poll"); err != nil {
				event.State, event.Error = "failed", err.Error()
				joined = errors.Join(joined, err)
			} else {
				event.State = "processed"
			}
			processed := time.Now().UTC()
			event.ProcessedAt = &processed
			_ = s.Store.UpdateWorkflowEvent(ctx, event)
		}
	}
	return joined
}

func pollDue(source core.ConfigSource, now time.Time) bool {
	interval := source.PollIntervalSeconds
	if interval < 30 {
		interval = defaultPollInterval
	}
	return source.LastPolledAt == nil || now.Sub(*source.LastPolledAt) >= time.Duration(interval)*time.Second
}

func (s *Service) snapshotChanged(ctx context.Context, resource core.WorkflowResource, snapshot map[string]core.WorkflowSourceRevision) bool {
	revisions, err := s.Store.ListWorkflowRevisions(ctx, resource.ID, 1)
	if err != nil || len(revisions) == 0 {
		return true
	}
	if revisions[0].SpecDigest != resource.SpecDigest {
		return true
	}
	previous := revisions[0].Sources
	if len(previous) != len(snapshot) {
		return true
	}
	for name, source := range snapshot {
		if previous[name].CommitSHA != source.CommitSHA {
			return true
		}
	}
	return false
}

func snapshotDigest(snapshot map[string]core.WorkflowSourceRevision) string {
	contents, _ := json.Marshal(snapshot)
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func (s *Service) Activate(ctx context.Context, id string) (core.WorkflowResource, core.WorkflowRevision, error) {
	resource, err := s.Store.GetWorkflowResource(ctx, id)
	if err != nil {
		return resource, core.WorkflowRevision{}, err
	}
	resource.Active, resource.State, resource.LastError, resource.UpdatedAt = true, "ready", "", time.Now().UTC()
	if err := s.Store.UpdateWorkflowResource(ctx, resource); err != nil {
		return resource, core.WorkflowRevision{}, err
	}
	if resource.Kind != KindApplication {
		return resource, core.WorkflowRevision{}, nil
	}
	revision, err := s.Start(ctx, resource.ID, "activation")
	return resource, revision, err
}

func (s *Service) Deactivate(ctx context.Context, id string) (core.WorkflowResource, error) {
	resource, err := s.Store.GetWorkflowResource(ctx, id)
	if err != nil {
		return resource, err
	}
	resource.Active, resource.State, resource.UpdatedAt = false, "paused", time.Now().UTC()
	return resource, s.Store.UpdateWorkflowResource(ctx, resource)
}

func (s *Service) Start(ctx context.Context, resourceID, trigger string) (core.WorkflowRevision, error) {
	resource, err := s.Store.GetWorkflowResource(ctx, resourceID)
	if err != nil {
		return core.WorkflowRevision{}, err
	}
	if !resource.Active || resource.Kind != KindApplication {
		return core.WorkflowRevision{}, errors.New("application resource is not active")
	}
	source, err := s.Store.GetConfigSource(ctx, resource.ConfigSourceID)
	if err != nil {
		return core.WorkflowRevision{}, err
	}
	snapshot, err := s.resolveResourceSources(ctx, source, resource)
	if err != nil {
		return core.WorkflowRevision{}, err
	}
	return s.startWithSnapshot(ctx, resource, source, snapshot, trigger)
}

func (s *Service) startWithSnapshot(ctx context.Context, resource core.WorkflowResource, source core.ConfigSource, snapshot map[string]core.WorkflowSourceRevision, trigger string) (core.WorkflowRevision, error) {
	revision := core.WorkflowRevision{ID: ulid.Make().String(), ResourceID: resource.ID, ConfigSHA: resource.ConfigSHA, SpecDigest: resource.SpecDigest,
		State: "queued", Trigger: trigger, Sources: snapshot, Outputs: map[string]map[string]string{}, CreatedAt: time.Now().UTC()}
	if err := s.Store.CreateWorkflowRevision(ctx, revision); err != nil {
		return revision, err
	}
	go s.runApplication(context.Background(), resource, source, revision)
	return revision, nil
}

func (s *Service) resolveResourceSources(ctx context.Context, source core.ConfigSource, resource core.WorkflowResource) (map[string]core.WorkflowSourceRevision, error) {
	documents, err := Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].Spec == nil {
		return nil, errors.New("stored application document is invalid")
	}
	result := map[string]core.WorkflowSourceRevision{}
	for alias, spec := range documents[0].Spec.Sources {
		sha, err := s.repositoryHead(ctx, source, spec.Repository, sourceRevisionRef(spec))
		if err != nil {
			return nil, fmt.Errorf("resolve source %s: %w", alias, err)
		}
		result[alias] = core.WorkflowSourceRevision{Alias: alias, Repository: spec.Repository, Branch: sourceRevisionRef(spec), CommitSHA: sha, Path: spec.Path}
	}
	return result, nil
}

func (s *Service) lock(key string) func() {
	s.mu.Lock()
	lock := s.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[key] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func sortedResourceNames(resources map[string]JobSpec) []string {
	names := make([]string, 0, len(resources))
	for name := range resources {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
