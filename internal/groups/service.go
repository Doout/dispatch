package groups

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/kubeconfig"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
	"gopkg.in/yaml.v3"
)

type Resolver interface {
	ResolvePullRequest(context.Context, string, int) (events.SourceRevision, error)
	ResolveBranch(context.Context, string, string) (events.SourceRevision, error)
}

type Notifier interface {
	UpdateComment(context.Context, string, int, string, string) (string, error)
}

type NamespaceCleaner interface {
	CleanupNamespace(context.Context, core.Server, string) error
}

type KubectlNamespaceCleaner struct{}

func (KubectlNamespaceCleaner) CleanupNamespace(ctx context.Context, server core.Server, namespace string) error {
	if server.Kubernetes == nil || namespace == "" {
		return nil
	}
	prepared, cleanup, err := kubeconfig.Prepare(*server.Kubernetes)
	if err != nil {
		return fmt.Errorf("prepare kubeconfig: %w", err)
	}
	defer cleanup()
	args := []string{"--kubeconfig", prepared.KubeconfigPath}
	if prepared.Context != "" {
		args = append(args, "--context", prepared.Context)
	}
	args = append(args, "delete", "namespace", namespace, "--ignore-not-found=true", "--wait=true")
	output, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("delete namespace: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

type Service struct {
	store       store.Store
	deployments *deploy.Service
	resolver    Resolver
	notifier    Notifier
	cleaner     NamespaceCleaner
	pollEvery   time.Duration
	mu          sync.Mutex
	locks       map[string]*sync.Mutex
}

func New(data store.Store, deployments *deploy.Service, resolver Resolver, notifier Notifier, cleaner NamespaceCleaner) *Service {
	if cleaner == nil {
		cleaner = KubectlNamespaceCleaner{}
	}
	return &Service{store: data, deployments: deployments, resolver: resolver, notifier: notifier, cleaner: cleaner,
		pollEvery: time.Second, locks: map[string]*sync.Mutex{}}
}

func (s *Service) Process(ctx context.Context, event core.IncomingEvent) ([]core.PreviewGroupRun, error) {
	if event.Kind == core.EventKindPullRequestComment && event.Action == "created" && event.Command != "" {
		if !event.TrustedActor {
			return nil, nil
		}
		groups, err := s.store.MatchingPreviewGroups(ctx, event.Provider, event.Repository, event.Command, event.ProviderConnectionID)
		if err != nil {
			return nil, err
		}
		items := []core.PreviewGroupRun{}
		for _, group := range groups {
			created, err := s.store.RecordPreviewGroupDelivery(ctx, event.Provider, event.DeliveryID, group.ID, event.ReceivedAt)
			if err != nil {
				return items, err
			}
			if !created {
				continue
			}
			run, err := s.handleCommand(ctx, group, event)
			if err != nil {
				_ = s.notifyCommandError(ctx, group, event, err)
				return items, err
			}
			items = append(items, run)
		}
		return items, nil
	}
	if event.Kind == core.EventKindPullRequest && event.Action == "closed" {
		groups, err := s.store.ListPreviewGroups(ctx)
		if err != nil {
			return nil, err
		}
		items := []core.PreviewGroupRun{}
		for _, group := range groups {
			if group.GitHubAppID != event.ProviderConnectionID {
				continue
			}
			run, err := s.store.FindPreviewGroupRunForPR(ctx, group.ID, event.Repository, event.PullRequestNumber)
			if err != nil {
				return items, err
			}
			if run == nil {
				continue
			}
			go s.closeSource(run.ID, event.Repository, event.PullRequestNumber)
			items = append(items, *run)
		}
		return items, nil
	}
	return nil, nil
}

func (s *Service) closeSource(runID, repository string, number int) {
	ctx := context.Background()
	run, err := s.store.GetPreviewGroupRun(ctx, runID)
	if err != nil {
		return
	}
	lock := s.runLock("group:" + run.GroupID)
	lock.Lock()
	defer lock.Unlock()
	run, err = s.store.GetPreviewGroupRun(ctx, runID)
	if err != nil || run.State == core.PreviewGroupClosed {
		return
	}
	now, allClosed := time.Now().UTC(), true
	for index := range run.Sources {
		if run.Sources[index].Repository == repository && run.Sources[index].PullRequest == number {
			run.Sources[index].ClosedAt = &now
		}
		if run.Sources[index].PullRequest > 0 && run.Sources[index].ClosedAt == nil {
			allClosed = false
		}
	}
	if err := s.store.ReplacePreviewGroupSources(ctx, run.ID, run.Sources); err != nil {
		return
	}
	if allClosed {
		_ = s.cleanupRun(ctx, &run, true)
	} else {
		s.notifyRun(run.ID)
	}
}

type linkReference struct {
	key    string
	number int
}

func parseLinks(arguments string) ([]linkReference, error) {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return nil, nil
	}
	if !strings.HasPrefix(strings.ToLower(arguments), "with ") {
		return nil, errors.New("use 'with alias=#123' to attach another pull request")
	}
	parts := strings.FieldsFunc(strings.TrimSpace(arguments[5:]), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	items, seen := []linkReference{}, map[string]bool{}
	for _, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("invalid linked pull request %q", part)
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimPrefix(strings.TrimSpace(value), "#")
		number, err := strconv.Atoi(value)
		if err != nil || number < 1 {
			return nil, fmt.Errorf("invalid pull request number in %q", part)
		}
		if seen[key] {
			return nil, fmt.Errorf("linked component %q was provided more than once", key)
		}
		seen[key] = true
		items = append(items, linkReference{key: key, number: number})
	}
	return items, nil
}

func (s *Service) handleCommand(ctx context.Context, group core.PreviewGroup, event core.IncomingEvent) (core.PreviewGroupRun, error) {
	commandLock := s.runLock("command:" + group.ID + ":" + event.Repository + ":" + strconv.Itoa(event.PullRequestNumber))
	commandLock.Lock()
	defer commandLock.Unlock()
	if s.resolver == nil {
		return core.PreviewGroupRun{}, errors.New("source resolver is not configured")
	}
	links, err := parseLinks(event.Arguments)
	if err != nil {
		return core.PreviewGroupRun{}, err
	}
	origin, ok := componentForRepository(group, event.Repository)
	if !ok {
		return core.PreviewGroupRun{}, errors.New("the command repository is not a component of this group")
	}
	explicit := map[string]int{origin.Alias: event.PullRequestNumber}
	for _, link := range links {
		component, err := resolveComponentKey(group, link.key)
		if err != nil {
			return core.PreviewGroupRun{}, err
		}
		if existing, found := explicit[component.Alias]; found && existing != link.number {
			return core.PreviewGroupRun{}, fmt.Errorf("component %q has conflicting pull request references", component.Alias)
		}
		explicit[component.Alias] = link.number
	}

	sources := make([]core.PreviewGroupSource, 0, len(group.Components))
	for _, component := range group.Components {
		source := core.PreviewGroupSource{ID: ulid.Make().String(), GroupID: group.ID, ComponentID: component.ID,
			Alias: component.Alias, Repository: component.Repository}
		if number, found := explicit[component.Alias]; found {
			revision, err := s.resolver.ResolvePullRequest(ctx, component.Repository, number)
			if err != nil {
				return core.PreviewGroupRun{}, fmt.Errorf("resolve %s pull request #%d: %w", component.Alias, number, err)
			}
			if !revision.Open {
				return core.PreviewGroupRun{}, fmt.Errorf("%s pull request #%d is closed", component.Alias, number)
			}
			source.PullRequest, source.HeadRef, source.SHA, source.BaseRef = number, revision.HeadRef, revision.HeadSHA, revision.BaseRef
		} else {
			revision, err := s.resolver.ResolveBranch(ctx, component.Repository, component.DefaultBranch)
			if err != nil {
				return core.PreviewGroupRun{}, fmt.Errorf("resolve %s default branch: %w", component.Alias, err)
			}
			source.HeadRef, source.SHA, source.DefaultBranch = component.DefaultBranch, revision.HeadSHA, true
		}
		sources = append(sources, source)
	}

	originRun, err := s.store.FindPreviewGroupRunForPR(ctx, group.ID, origin.Repository, event.PullRequestNumber)
	if err != nil {
		return core.PreviewGroupRun{}, err
	}
	var losingRun *core.PreviewGroupRun
	for sourceIndex := range sources {
		source := sources[sourceIndex]
		if source.PullRequest == 0 {
			continue
		}
		found, err := s.store.FindPreviewGroupRunForPR(ctx, group.ID, source.Repository, source.PullRequest)
		if err != nil {
			return core.PreviewGroupRun{}, err
		}
		if found != nil {
			for _, existing := range found.Sources {
				if existing.Repository == source.Repository && existing.PullRequest == source.PullRequest {
					sources[sourceIndex].StatusCommentID = existing.StatusCommentID
				}
			}
		}
		if found != nil && (originRun == nil || found.ID != originRun.ID) {
			if losingRun != nil && losingRun.ID != found.ID {
				return core.PreviewGroupRun{}, errors.New("linked pull requests span more than two active environments")
			}
			losingRun = found
		}
	}
	now := time.Now().UTC()
	newRun := originRun == nil
	if originRun == nil {
		runID := ulid.Make().String()
		slug := stableSlug(group.Name+"-pr-"+strconv.Itoa(event.PullRequestNumber), runID)
		for index := range sources {
			sources[index].RunID = runID
		}
		persistedSources := sources
		if losingRun != nil {
			persistedSources = make([]core.PreviewGroupSource, 0, len(sources))
			for _, source := range sources {
				claimed := false
				for _, existing := range losingRun.Sources {
					if source.PullRequest > 0 && source.Repository == existing.Repository && source.PullRequest == existing.PullRequest {
						claimed = true
					}
				}
				if !claimed {
					persistedSources = append(persistedSources, source)
				}
			}
		}
		originRun = &core.PreviewGroupRun{ID: runID, GroupID: group.ID, Slug: slug, Namespace: trimName("dispatch-"+slug, 63),
			State: core.PreviewGroupRequested, Message: "Preview group accepted", Sources: persistedSources,
			HookEnvironment: event.HookEnvironment(), CreatedAt: now, UpdatedAt: now}
		if err := s.store.CreatePreviewGroupRun(ctx, *originRun); err != nil {
			return core.PreviewGroupRun{}, err
		}
		s.notifyRun(originRun.ID)
	} else {
		originRun.HookEnvironment = event.HookEnvironment()
		originRun.UpdatedAt = now
		if err := s.store.UpdatePreviewGroupRun(ctx, *originRun); err != nil {
			return core.PreviewGroupRun{}, err
		}
	}
	if !newRun && losingRun == nil {
		previous := append([]core.PreviewGroupSource(nil), originRun.Sources...)
		for index := range sources {
			sources[index].RunID = originRun.ID
		}
		if err := s.store.ReplacePreviewGroupSources(ctx, originRun.ID, sources); err != nil {
			return core.PreviewGroupRun{}, err
		}
		originRun.Sources = sources
		go s.runAttempt(originRun.ID, group, sources, previous, "")
		return *originRun, nil
	}
	previous := append([]core.PreviewGroupSource(nil), originRun.LastSuccessfulSources...)
	if len(previous) == 0 {
		previous = append(previous, originRun.Sources...)
	}
	go s.runAttempt(originRun.ID, group, sources, previous, func() string {
		if losingRun != nil {
			return losingRun.ID
		}
		return ""
	}())
	return *originRun, nil
}

func componentForRepository(group core.PreviewGroup, repository string) (core.PreviewGroupComponent, bool) {
	repository = events.NormalizeRepository(repository)
	for _, component := range group.Components {
		if component.Repository == repository {
			return component, true
		}
	}
	return core.PreviewGroupComponent{}, false
}

func resolveComponentKey(group core.PreviewGroup, key string) (core.PreviewGroupComponent, error) {
	key = events.NormalizeRepository(key)
	matches := []core.PreviewGroupComponent{}
	for _, component := range group.Components {
		_, short, _ := strings.Cut(component.Repository, "/")
		if component.Alias == key || component.Repository == key || short == key {
			matches = append(matches, component)
		}
	}
	if len(matches) == 0 {
		return core.PreviewGroupComponent{}, fmt.Errorf("unknown component %q", key)
	}
	if len(matches) > 1 {
		return core.PreviewGroupComponent{}, fmt.Errorf("component %q is ambiguous; use an alias or full repository", key)
	}
	return matches[0], nil
}

func (s *Service) runLock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks[id] == nil {
		s.locks[id] = &sync.Mutex{}
	}
	return s.locks[id]
}

func (s *Service) runAttempt(runID string, group core.PreviewGroup, desired, previous []core.PreviewGroupSource, losingRunID string) {
	lock := s.runLock("group:" + group.ID)
	lock.Lock()
	defer lock.Unlock()
	ctx := context.Background()
	run, err := s.store.GetPreviewGroupRun(ctx, runID)
	if err != nil {
		return
	}
	run.Attempt++
	run.State, run.Message, run.UpdatedAt = core.PreviewGroupDeploying, "Deploying preview components", time.Now().UTC()
	attempt := core.PreviewGroupAttempt{ID: ulid.Make().String(), RunID: run.ID, Sequence: run.Attempt,
		State: run.State, Configuration: group, DesiredSources: desired, PreviousSources: previous, CreatedAt: run.UpdatedAt}
	_ = s.store.CreatePreviewGroupAttempt(ctx, attempt)
	_ = s.store.UpdatePreviewGroupRun(ctx, run)
	s.notifyRun(run.ID)

	oldComponents := append([]core.PreviewGroupRunComponent(nil), run.Components...)
	if err := s.store.ClearPreviewGroupRunComponents(ctx, run.ID); err != nil {
		run.State, run.Message, run.UpdatedAt = core.PreviewGroupFailed, err.Error(), time.Now().UTC()
		_ = s.store.UpdatePreviewGroupRun(ctx, run)
		return
	}
	deployed, deployErr := s.deployRevisionSet(ctx, &run, group, desired)
	if deployErr == nil {
		var mergeCleanupErr error
		if losingRunID != "" {
			if losingRun, loadErr := s.store.GetPreviewGroupRun(ctx, losingRunID); loadErr == nil {
				mergeCleanupErr = s.cleanupRun(ctx, &losingRun, false)
			} else {
				mergeCleanupErr = loadErr
			}
		}
		for index := range desired {
			desired[index].RunID = run.ID
		}
		if err := s.store.ReplacePreviewGroupSources(ctx, run.ID, desired); err != nil {
			run.State, run.Message, run.UpdatedAt = core.PreviewGroupDegraded, "Components are ready, but linked pull request state could not be saved: "+err.Error(), time.Now().UTC()
			_ = s.store.UpdatePreviewGroupRun(ctx, run)
			attempt.State, attempt.Message = run.State, run.Message
			now := time.Now().UTC()
			attempt.FinishedAt = &now
			_ = s.store.UpdatePreviewGroupAttempt(ctx, attempt)
			s.notifyRun(run.ID)
			return
		}
		run.Sources, run.LastSuccessfulSources = desired, append([]core.PreviewGroupSource(nil), desired...)
		run.State, run.Message, run.UpdatedAt = core.PreviewGroupReady, "All preview components are ready", time.Now().UTC()
		if mergeCleanupErr != nil {
			run.Message = "Preview is ready. The previous environment could not be fully cleaned up: " + mergeCleanupErr.Error()
		}
		for _, component := range deployed {
			if entrypoint(group, component.Alias) {
				run.EntrypointURL = component.URL
			}
		}
		_ = s.store.UpdatePreviewGroupRun(ctx, run)
		attempt.State, attempt.Message = core.PreviewGroupReady, run.Message
		now := time.Now().UTC()
		attempt.FinishedAt = &now
		_ = s.store.UpdatePreviewGroupAttempt(ctx, attempt)
		for _, old := range oldComponents {
			if old.GeneratedAppID != "" && !containsGenerated(deployed, old.GeneratedAppID) {
				_ = s.store.DeleteApp(ctx, old.GeneratedAppID)
			}
		}
		s.notifyRun(run.ID)
		return
	}

	if len(run.LastSuccessfulSources) == 0 {
		_ = s.cleanupComponents(ctx, deployed)
		_ = s.cleanupNamespace(ctx, group, run.Namespace)
		run.State, run.Message = core.PreviewGroupFailed, deployErr.Error()
	} else {
		run.State, run.Message, run.UpdatedAt = core.PreviewGroupRollingBack, "Update failed. Restoring the last successful revision set", time.Now().UTC()
		_ = s.store.UpdatePreviewGroupRun(ctx, run)
		restored, restoreErr := s.deployRevisionSet(ctx, &run, group, previous)
		if restoreErr != nil {
			run.State, run.Message = core.PreviewGroupDegraded, fmt.Sprintf("update failed: %v; restoration failed: %v", deployErr, restoreErr)
		} else {
			run.State, run.Message, run.Sources = core.PreviewGroupReady, "Previous revision set restored after a failed update", previous
			if sourceErr := s.store.ReplacePreviewGroupSources(ctx, run.ID, previous); sourceErr != nil {
				run.State, run.Message = core.PreviewGroupDegraded, "Previous components were restored, but their source state could not be saved: "+sourceErr.Error()
			}
			for _, component := range restored {
				if entrypoint(group, component.Alias) {
					run.EntrypointURL = component.URL
				}
			}
			if run.State == core.PreviewGroupReady {
				for _, component := range deployed {
					if component.GeneratedAppID != "" {
						_ = s.store.DeleteApp(ctx, component.GeneratedAppID)
					}
				}
				for _, component := range oldComponents {
					if component.GeneratedAppID != "" {
						_ = s.store.DeleteApp(ctx, component.GeneratedAppID)
					}
				}
			}
		}
	}
	run.UpdatedAt = time.Now().UTC()
	_ = s.store.UpdatePreviewGroupRun(ctx, run)
	attempt.State, attempt.Message = run.State, run.Message
	now := time.Now().UTC()
	attempt.FinishedAt = &now
	_ = s.store.UpdatePreviewGroupAttempt(ctx, attempt)
	s.notifyRun(run.ID)
}

type componentResult struct {
	component core.PreviewGroupRunComponent
	err       error
}

func (s *Service) deployRevisionSet(ctx context.Context, run *core.PreviewGroupRun, group core.PreviewGroup, sources []core.PreviewGroupSource) ([]core.PreviewGroupRunComponent, error) {
	levels := dependencyLevels(group.Components)
	outputs := map[string]map[string]string{}
	deployed := []core.PreviewGroupRunComponent{}
	for _, level := range levels {
		results := make(chan componentResult, len(level))
		var wait sync.WaitGroup
		for _, component := range level {
			component := component
			wait.Add(1)
			go func() {
				defer wait.Done()
				item, err := s.deployComponent(ctx, *run, group, component, sourceForAlias(sources, component.Alias), outputs)
				results <- componentResult{item, err}
			}()
		}
		wait.Wait()
		close(results)
		for result := range results {
			deployed = append(deployed, result.component)
			if result.err != nil {
				return deployed, fmt.Errorf("%s: %w", result.component.Alias, result.err)
			}
			outputs[result.component.Alias] = result.component.Outputs
		}
	}
	return deployed, nil
}

func (s *Service) deployComponent(ctx context.Context, run core.PreviewGroupRun, group core.PreviewGroup, component core.PreviewGroupComponent, source core.PreviewGroupSource, available map[string]map[string]string) (core.PreviewGroupRunComponent, error) {
	template, err := s.store.GetApp(ctx, component.AppID)
	item := core.PreviewGroupRunComponent{ID: ulid.Make().String(), RunID: run.ID, ComponentID: component.ID, Alias: component.Alias, State: "deploying"}
	if err != nil {
		item.State, item.Message = "failed", err.Error()
		return item, err
	}
	app := template
	app.PreDeployHook, app.PostDeployHook = component.PreDeployHook, component.PostDeployHook
	app.ID, app.Generated, app.Template = ulid.Make().String(), true, false
	app.Name = trimName(group.Name+"-"+run.Slug+"-"+component.Alias+"-"+strconv.Itoa(run.Attempt), 56) + "-" + strings.ToLower(app.ID[len(app.ID)-6:])
	app.Branch, app.HelmNamespace = source.HeadRef, run.Namespace
	app.HelmRelease = trimName("dispatch-"+run.Slug+"-"+component.Alias, 53)
	app.Domain = renderDomain(template.Domain, run, component, source)
	app.HelmGroupValues, err = bindingValues(component.Bindings, available)
	if err != nil {
		return item, err
	}
	componentOutputs := map[string]map[string]string{}
	for alias, values := range available {
		componentOutputs[alias] = values
	}
	componentOutputs[component.Alias] = map[string]string{
		"url": publicURL(app.Domain), "host": host(publicURL(app.Domain)), "namespace": run.Namespace, "release": app.HelmRelease,
	}
	app.HookEnvironment = componentEnvironment(componentOutputs)
	for name, value := range run.HookEnvironment {
		app.HookEnvironment[name] = value
	}
	app.HookEnvironment["DISPATCH_EVENT_COMPONENT_ALIAS"] = component.Alias
	app.HookEnvironment["DISPATCH_EVENT_COMPONENT_REPOSITORY"] = component.Repository
	for _, secretID := range component.SecretIDs {
		secret, secretErr := s.store.GetSecret(ctx, secretID)
		if secretErr != nil {
			return item, fmt.Errorf("load hook credential for %s: %w", component.Alias, secretErr)
		}
		app.HookEnvironment[core.SecretEnvironmentKey(secret.ID, secret.EnvironmentVariable)] = secret.EncryptedValue
	}
	app.State, app.CreatedAt = "preview-group", time.Now().UTC()
	item.GeneratedAppID, item.URL = app.ID, publicURL(app.Domain)
	item.Outputs = map[string]string{"url": item.URL, "host": host(item.URL), "namespace": run.Namespace, "release": app.HelmRelease}
	if err := s.store.CreateApp(ctx, app); err != nil {
		item.State, item.Message = "failed", err.Error()
		return item, err
	}
	if err := s.store.UpsertPreviewGroupRunComponent(ctx, item); err != nil {
		return item, err
	}
	deployment, err := s.deployments.Start(ctx, app.ID, source.SHA)
	if err != nil {
		item.State, item.Message = "failed", err.Error()
		_ = s.store.UpsertPreviewGroupRunComponent(ctx, item)
		return item, err
	}
	item.DeploymentID = deployment.ID
	_ = s.store.UpsertPreviewGroupRunComponent(ctx, item)
	deployment, err = s.waitDeployment(ctx, deployment.ID)
	if err != nil {
		item.State, item.Message = "failed", err.Error()
		_ = s.store.UpsertPreviewGroupRunComponent(ctx, item)
		return item, err
	}
	if deployment.State != core.DeploymentSucceeded {
		err = errors.New(deployment.Message)
		item.State, item.Message = "failed", deployment.Message
		_ = s.store.UpsertPreviewGroupRunComponent(ctx, item)
		return item, err
	}
	for key, value := range deployment.Outputs {
		item.Outputs[key] = value
	}
	if value := item.Outputs["url"]; value != "" {
		item.URL, item.Outputs["url"], item.Outputs["host"] = publicURL(value), publicURL(value), host(publicURL(value))
	}
	item.Outputs["namespace"], item.Outputs["release"] = run.Namespace, app.HelmRelease
	item.State, item.Message = "ready", "Component is ready"
	err = s.store.UpsertPreviewGroupRunComponent(ctx, item)
	return item, err
}

func (s *Service) waitDeployment(ctx context.Context, id string) (core.Deployment, error) {
	interval := s.pollEvery
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		deployment, err := s.store.GetDeployment(ctx, id)
		if err != nil {
			return deployment, err
		}
		if deployment.State.Terminal() {
			return deployment, nil
		}
		select {
		case <-ctx.Done():
			return deployment, ctx.Err()
		case <-ticker.C:
		}
	}
}

func dependencyLevels(components []core.PreviewGroupComponent) [][]core.PreviewGroupComponent {
	remaining, done := map[string]core.PreviewGroupComponent{}, map[string]bool{}
	for _, component := range components {
		remaining[component.Alias] = component
	}
	levels := [][]core.PreviewGroupComponent{}
	for len(remaining) > 0 {
		level := []core.PreviewGroupComponent{}
		for alias, component := range remaining {
			ready := true
			for _, dependency := range component.DependsOn {
				if !done[dependency] {
					ready = false
				}
			}
			if ready {
				level = append(level, component)
				delete(remaining, alias)
			}
		}
		sort.Slice(level, func(i, j int) bool { return level[i].Alias < level[j].Alias })
		if len(level) == 0 {
			break
		}
		for _, component := range level {
			done[component.Alias] = true
		}
		levels = append(levels, level)
	}
	return levels
}

func bindingValues(bindings []core.PreviewGroupBinding, available map[string]map[string]string) (string, error) {
	root := map[string]any{}
	for _, binding := range bindings {
		alias, output, _ := strings.Cut(binding.Source, ".")
		value, ok := available[alias][output]
		if !ok {
			return "", fmt.Errorf("binding source %s has no output", binding.Source)
		}
		parts := strings.Split(binding.HelmValuePath, ".")
		cursor := root
		for _, part := range parts[:len(parts)-1] {
			child, ok := cursor[part].(map[string]any)
			if !ok {
				child = map[string]any{}
				cursor[part] = child
			}
			cursor = child
		}
		cursor[parts[len(parts)-1]] = value
	}
	if len(root) == 0 {
		return "", nil
	}
	encoded, err := yaml.Marshal(root)
	return string(encoded), err
}

var environmentPart = regexp.MustCompile(`[^A-Z0-9]+`)
var dnsPart = regexp.MustCompile(`[^a-z0-9-]+`)
var camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)

func componentEnvironment(outputs map[string]map[string]string) map[string]string {
	environment := map[string]string{}
	for alias, values := range outputs {
		for key, value := range values {
			name := "DISPATCH_COMPONENT_" + normalizeEnvironmentPart(alias) + "_" + normalizeEnvironmentPart(key)
			environment[name] = value
		}
	}
	return environment
}

func normalizeEnvironmentPart(value string) string {
	value = camelBoundary.ReplaceAllString(value, "${1}_${2}")
	return strings.Trim(environmentPart.ReplaceAllString(strings.ToUpper(value), "_"), "_")
}

func (s *Service) Cleanup(ctx context.Context, runID string) error {
	run, err := s.store.GetPreviewGroupRun(ctx, runID)
	if err != nil {
		return err
	}
	lock := s.runLock("group:" + run.GroupID)
	lock.Lock()
	defer lock.Unlock()
	run, err = s.store.GetPreviewGroupRun(ctx, runID)
	if err != nil {
		return err
	}
	return s.cleanupRun(ctx, &run, true)
}

func (s *Service) cleanupRun(ctx context.Context, run *core.PreviewGroupRun, notify bool) error {
	if run.State == core.PreviewGroupClosed {
		return nil
	}
	now := time.Now().UTC()
	for index := range run.Sources {
		if run.Sources[index].PullRequest > 0 && run.Sources[index].ClosedAt == nil {
			run.Sources[index].ClosedAt = &now
		}
	}
	if err := s.store.ReplacePreviewGroupSources(ctx, run.ID, run.Sources); err != nil {
		return err
	}
	run.State, run.Message, run.UpdatedAt = core.PreviewGroupCleaning, "Removing preview group", now
	_ = s.store.UpdatePreviewGroupRun(ctx, *run)
	if err := s.cleanupComponents(ctx, run.Components); err != nil {
		run.State, run.Message = core.PreviewGroupDegraded, err.Error()
		run.UpdatedAt = time.Now().UTC()
		_ = s.store.UpdatePreviewGroupRun(ctx, *run)
		if notify {
			s.notifyRun(run.ID)
		}
		return err
	}
	group := core.PreviewGroup{}
	if run.Group != nil {
		group = *run.Group
	}
	if err := s.cleanupNamespace(ctx, group, run.Namespace); err != nil {
		run.State, run.Message = core.PreviewGroupDegraded, err.Error()
		run.UpdatedAt = time.Now().UTC()
		_ = s.store.UpdatePreviewGroupRun(ctx, *run)
		if notify {
			s.notifyRun(run.ID)
		}
		return err
	}
	now = time.Now().UTC()
	run.State, run.Message, run.ClosedAt, run.UpdatedAt = core.PreviewGroupClosed, "Preview group removed", &now, now
	if err := s.store.UpdatePreviewGroupRun(ctx, *run); err != nil {
		return err
	}
	if notify {
		s.notifyRun(run.ID)
	}
	return nil
}

func (s *Service) cleanupComponents(ctx context.Context, components []core.PreviewGroupRunComponent) error {
	var failures []error
	for index := len(components) - 1; index >= 0; index-- {
		item := components[index]
		if item.GeneratedAppID == "" {
			continue
		}
		if err := s.deployments.Cleanup(ctx, item.GeneratedAppID, nil); err != nil && !errors.Is(err, store.ErrNotFound) {
			failures = append(failures, fmt.Errorf("cleanup %s: %w", item.Alias, err))
			continue
		}
		if err := s.store.DeleteApp(ctx, item.GeneratedAppID); err != nil && !errors.Is(err, store.ErrNotFound) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Service) cleanupNamespace(ctx context.Context, group core.PreviewGroup, namespace string) error {
	if len(group.Components) == 0 {
		return nil
	}
	app, err := s.store.GetApp(ctx, group.Components[0].AppID)
	if err != nil {
		return err
	}
	server, err := s.store.GetServer(ctx, app.ServerID)
	if err != nil {
		return err
	}
	return s.cleaner.CleanupNamespace(ctx, server, namespace)
}

func (s *Service) notifyCommandError(ctx context.Context, group core.PreviewGroup, event core.IncomingEvent, cause error) error {
	if s.notifier == nil {
		return nil
	}
	body := fmt.Sprintf("<!-- dispatch-preview-group-error -->\n### %s\n\n**Status:** Error\n\n%s", group.Name, cause.Error())
	_, err := s.notifier.UpdateComment(ctx, event.Repository, event.PullRequestNumber, "", body)
	return err
}

func (s *Service) notifyRun(runID string) {
	if s.notifier == nil {
		return
	}
	run, err := s.store.GetPreviewGroupRun(context.Background(), runID)
	if err != nil {
		return
	}
	body := groupComment(run)
	changed := false
	for index := range run.Sources {
		source := &run.Sources[index]
		if source.PullRequest == 0 {
			continue
		}
		commentID, err := s.notifier.UpdateComment(context.Background(), source.Repository, source.PullRequest, source.StatusCommentID, body)
		if err == nil && commentID != "" && commentID != source.StatusCommentID {
			source.StatusCommentID = commentID
			changed = true
		}
	}
	if changed {
		_ = s.store.ReplacePreviewGroupSources(context.Background(), run.ID, run.Sources)
	}
}

func groupComment(run core.PreviewGroupRun) string {
	name := "Preview group"
	if run.Group != nil {
		name = run.Group.Name
	}
	var body strings.Builder
	body.WriteString("<!-- dispatch-preview-group:")
	body.WriteString(run.ID)
	body.WriteString(" -->\n### ")
	body.WriteString(name)
	body.WriteString("\n\n")
	body.WriteString("**Status:** ")
	body.WriteString(titleState(string(run.State)))
	body.WriteString("\n")
	if run.EntrypointURL != "" {
		body.WriteString("**URL:** [Open preview](")
		body.WriteString(run.EntrypointURL)
		body.WriteString(")\n")
	}
	if run.Message != "" {
		body.WriteString("\n")
		body.WriteString(run.Message)
		body.WriteString("\n")
	}
	body.WriteString("\n| Component | Source | Revision | State |\n| --- | --- | --- | --- |\n")
	for _, source := range run.Sources {
		sourceText := source.HeadRef
		if source.PullRequest > 0 {
			sourceText = "PR #" + strconv.Itoa(source.PullRequest)
		} else if source.DefaultBranch {
			sourceText += " (default)"
		}
		state := "pending"
		for _, component := range run.Components {
			if component.Alias == source.Alias {
				state = component.State
			}
		}
		body.WriteString("| ")
		body.WriteString(source.Alias)
		body.WriteString(" | ")
		body.WriteString(sourceText)
		body.WriteString(" | `")
		body.WriteString(shortSHA(source.SHA))
		body.WriteString("` | ")
		body.WriteString(state)
		body.WriteString(" |\n")
	}
	open := []string{}
	for _, source := range run.Sources {
		if source.PullRequest > 0 && source.ClosedAt == nil {
			open = append(open, source.Repository+"#"+strconv.Itoa(source.PullRequest))
		}
	}
	if len(open) > 0 {
		body.WriteString("\n**Open pull requests:** ")
		body.WriteString(strings.Join(open, ", "))
		body.WriteString("\n")
	}
	if run.Group != nil {
		body.WriteString("\n**Link another pull request:**\n")
		for _, component := range run.Group.Components {
			body.WriteString("- `")
			body.WriteString(run.Group.Command)
			body.WriteString(" with ")
			body.WriteString(component.Alias)
			body.WriteString("=#123` for `")
			body.WriteString(component.Repository)
			body.WriteString("`\n")
		}
	}
	return body.String()
}

func sourceForAlias(sources []core.PreviewGroupSource, alias string) core.PreviewGroupSource {
	for _, source := range sources {
		if source.Alias == alias {
			return source
		}
	}
	return core.PreviewGroupSource{}
}
func entrypoint(group core.PreviewGroup, alias string) bool {
	for _, component := range group.Components {
		if component.Alias == alias {
			return component.Entrypoint
		}
	}
	return false
}
func containsGenerated(items []core.PreviewGroupRunComponent, id string) bool {
	for _, item := range items {
		if item.GeneratedAppID == id {
			return true
		}
	}
	return false
}
func shortSHA(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
func stableSlug(value, id string) string {
	value = trimName(value, 48)
	suffix := strings.ToLower(id)
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return trimName(value+"-"+suffix, 57)
}
func trimName(value string, limit int) string {
	value = strings.Trim(dnsPart.ReplaceAllString(strings.ToLower(value), "-"), "-")
	if len(value) > limit {
		value = strings.Trim(value[:limit], "-")
	}
	if value == "" {
		return "preview"
	}
	return value
}

var previewPRPattern = regexp.MustCompile(`-pr-([0-9]+)(?:-|$)`)

func renderDomain(value string, run core.PreviewGroupRun, component core.PreviewGroupComponent, source core.PreviewGroupSource) string {
	previewPR := source.PullRequest
	if match := previewPRPattern.FindStringSubmatch(run.Slug); len(match) == 2 {
		previewPR, _ = strconv.Atoi(match[1])
	}
	return strings.NewReplacer("{preview}", run.Slug, "{component}", component.Alias, "{pr}", strconv.Itoa(previewPR), "{branch}", trimName(source.HeadRef, 30), "{sha}", shortSHA(source.SHA)).Replace(value)
}
func publicURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return value
	}
	return "https://" + value
}
func host(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}
func titleState(value string) string {
	value = strings.ReplaceAll(value, "_", " ")
	if value == "" {
		return "Unknown"
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
