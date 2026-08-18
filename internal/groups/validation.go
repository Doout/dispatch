package groups

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/store"
)

var (
	aliasPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	valuePathPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$`)
)

func Validate(ctx context.Context, data store.Store, group *core.PreviewGroup, defaultCommand string) error {
	group.Name = strings.TrimSpace(group.Name)
	if group.Name == "" || len(group.Name) > 80 {
		return errors.New("group name must be between 1 and 80 characters")
	}
	command, err := events.NormalizeCommand(group.Command, defaultCommand)
	if err != nil {
		return err
	}
	group.Command = command
	if group.GitHubAppID != "" {
		connection, err := data.GetGitHubApp(ctx, group.GitHubAppID)
		if err != nil {
			return errors.New("selected GitHub App connection no longer exists")
		}
		if connection.InstallationID < 1 {
			return errors.New("selected GitHub App must be installed before it can receive preview commands")
		}
	}
	if len(group.Components) == 0 {
		return errors.New("at least one component is required")
	}

	aliases, repositories := map[string]bool{}, map[string]bool{}
	entrypoints, serverID := 0, ""
	for index := range group.Components {
		component := &group.Components[index]
		component.GroupID = group.ID
		component.Alias = strings.ToLower(strings.TrimSpace(component.Alias))
		component.Repository = events.NormalizeRepository(component.Repository)
		component.DefaultBranch = strings.TrimSpace(component.DefaultBranch)
		if !aliasPattern.MatchString(component.Alias) {
			return fmt.Errorf("component alias %q must start with a letter and use lowercase letters, numbers, or hyphens", component.Alias)
		}
		if aliases[component.Alias] {
			return fmt.Errorf("component alias %q is duplicated", component.Alias)
		}
		aliases[component.Alias] = true
		owner, name, ok := strings.Cut(component.Repository, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return fmt.Errorf("component %q must use an owner/repository identifier", component.Alias)
		}
		if repositories[component.Repository] {
			return fmt.Errorf("repository %q is duplicated", component.Repository)
		}
		repositories[component.Repository] = true
		if component.DefaultBranch == "" {
			return fmt.Errorf("component %q requires a default branch", component.Alias)
		}
		if len(component.PreDeployHook) > 64<<10 || len(component.PostDeployHook) > 64<<10 {
			return fmt.Errorf("component %q deployment hooks must each be no larger than 64 KiB", component.Alias)
		}
		seenSecrets := map[string]bool{}
		for _, secretID := range component.SecretIDs {
			if secretID == "" || seenSecrets[secretID] {
				return fmt.Errorf("component %q secret bindings must be unique", component.Alias)
			}
			seenSecrets[secretID] = true
			if _, err := data.GetSecret(ctx, secretID); err != nil {
				return fmt.Errorf("component %q has a secret that no longer exists", component.Alias)
			}
		}
		app, err := data.GetApp(ctx, component.AppID)
		if err != nil {
			return fmt.Errorf("load component %q application: %w", component.Alias, err)
		}
		if app.BuildType != core.BuildTypeHelm {
			return fmt.Errorf("component %q must use a Helm application", component.Alias)
		}
		server, err := data.GetServer(ctx, app.ServerID)
		if err != nil {
			return fmt.Errorf("load component %q server: %w", component.Alias, err)
		}
		if !core.IsKubernetesRuntime(server.Runtime) {
			return fmt.Errorf("component %q must target Kubernetes", component.Alias)
		}
		if serverID == "" {
			serverID = app.ServerID
		} else if serverID != app.ServerID {
			return errors.New("all components must target the same Kubernetes server")
		}
		if component.Entrypoint {
			entrypoints++
		}
	}
	if entrypoints != 1 {
		return errors.New("exactly one component must be the entrypoint")
	}

	for _, component := range group.Components {
		seenDependencies := map[string]bool{}
		bindingPaths := []string{}
		for _, dependency := range component.DependsOn {
			if dependency == component.Alias || !aliases[dependency] {
				return fmt.Errorf("component %q has an invalid dependency %q", component.Alias, dependency)
			}
			if seenDependencies[dependency] {
				return fmt.Errorf("component %q repeats dependency %q", component.Alias, dependency)
			}
			seenDependencies[dependency] = true
		}
		for _, binding := range component.Bindings {
			sourceAlias, output, ok := strings.Cut(binding.Source, ".")
			if !ok || !aliases[sourceAlias] || sourceAlias == component.Alias || strings.TrimSpace(output) == "" {
				return fmt.Errorf("component %q has invalid binding source %q", component.Alias, binding.Source)
			}
			if !valuePathPattern.MatchString(binding.HelmValuePath) {
				return fmt.Errorf("component %q has invalid Helm value path %q", component.Alias, binding.HelmValuePath)
			}
			for _, existing := range bindingPaths {
				if existing == binding.HelmValuePath || strings.HasPrefix(existing, binding.HelmValuePath+".") || strings.HasPrefix(binding.HelmValuePath, existing+".") {
					return fmt.Errorf("component %q has conflicting Helm value paths %q and %q", component.Alias, existing, binding.HelmValuePath)
				}
			}
			bindingPaths = append(bindingPaths, binding.HelmValuePath)
			if !dependsOnAlias(group.Components, component.Alias, sourceAlias, map[string]bool{}) {
				return fmt.Errorf("component %q must depend on binding source %q", component.Alias, sourceAlias)
			}
		}
	}
	if err := validateAcyclic(group.Components); err != nil {
		return err
	}

	existing, err := data.ListPreviewGroups(ctx)
	if err != nil {
		return err
	}
	if group.Enabled {
		for _, other := range existing {
			if other.ID == group.ID || !other.Enabled || other.Command != group.Command || other.GitHubAppID != group.GitHubAppID {
				continue
			}
			for _, left := range group.Components {
				for _, right := range other.Components {
					if left.Repository == right.Repository {
						return store.ErrPreviewGroupOverlap
					}
				}
			}
		}
	}
	return nil
}

func dependsOnAlias(components []core.PreviewGroupComponent, componentAlias, target string, seen map[string]bool) bool {
	if seen[componentAlias] {
		return false
	}
	seen[componentAlias] = true
	for _, component := range components {
		if component.Alias != componentAlias {
			continue
		}
		for _, dependency := range component.DependsOn {
			if dependency == target || dependsOnAlias(components, dependency, target, seen) {
				return true
			}
		}
	}
	return false
}

func validateAcyclic(components []core.PreviewGroupComponent) error {
	graph := map[string][]string{}
	for _, component := range components {
		graph[component.Alias] = component.DependsOn
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(alias string) error {
		if visiting[alias] {
			return errors.New("component dependency graph contains a cycle")
		}
		if visited[alias] {
			return nil
		}
		visiting[alias] = true
		for _, dependency := range graph[alias] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[alias], visited[alias] = false, true
		return nil
	}
	for alias := range graph {
		if err := visit(alias); err != nil {
			return err
		}
	}
	return nil
}
