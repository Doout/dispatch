package workflow

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/store"
)

func (s *Service) resolveWorkflowSource(ctx context.Context, config core.ConfigSource, templateAppID, alias string, spec SourceSpec) (core.WorkflowSourceRevision, error) {
	item := core.WorkflowSourceRevision{Alias: alias, Repository: spec.Repository, Branch: sourceRevisionRef(spec), Path: spec.Path}
	binding, found, err := s.lookupWorkflowRepositoryConnection(ctx, config.ProjectID, spec)
	if err != nil {
		return item, err
	}
	if found {
		item.RepositoryConnectionID, item.CredentialSecretID = binding.ID, binding.CredentialSecretID
		item.CloneURL, item.GitHubAppID = binding.RepositoryURL, binding.GitHubAppID
		source, err := s.boundSourceConfig(ctx, config, binding)
		if err != nil {
			return item, err
		}
		item.CommitSHA, err = s.repositoryHead(ctx, source, item.CloneURL, item.Branch)
		return item, err
	}
	connection, err := s.workflowSourceApp(ctx, config.GitHubAppID, templateAppID, spec.Connection)
	if err != nil {
		return item, err
	}
	if connection.ID == "" {
		item.CommitSHA, err = s.repositoryHead(ctx, config, item.Repository, item.Branch)
		return item, err
	}
	item.GitHubAppID = connection.ID
	item.CloneURL, err = githubAppSourceURL(connection, item.Repository)
	if err != nil {
		return item, err
	}
	if s.GitHub == nil {
		return item, errors.New("GitHub App access is not configured")
	}
	item.CommitSHA, err = s.GitHub.RepositoryHead(ctx, connection.ID, item.CloneURL, item.Branch)
	return item, err
}

// A workflow may spell its repository as owner/name or as an absolute HTTPS URL.
func matchesBoundRepository(binding core.RepositoryConnection, repository string) bool {
	candidate := repository
	if !isCloneURL(repository) {
		origin, err := url.Parse(binding.RepositoryURL)
		if err != nil {
			return false
		}
		candidate = "https://" + origin.Host + "/" + repository
	}
	normalized, err := core.CanonicalRepositoryURL(candidate)
	return err == nil && normalized == binding.RepositoryURL
}

func (s *Service) lookupWorkflowRepositoryConnection(ctx context.Context, projectID string, spec SourceSpec) (core.RepositoryConnection, bool, error) {
	if spec.Connection == "" {
		return core.RepositoryConnection{}, false, nil
	}
	binding, err := s.Store.GetRepositoryConnectionByName(ctx, projectID, spec.Connection)
	if errors.Is(err, store.ErrNotFound) {
		return binding, false, nil
	}
	if err != nil {
		return binding, false, err
	}
	if binding.ProjectID != projectID || !matchesBoundRepository(binding, spec.Repository) {
		return binding, false, fmt.Errorf("repository does not match connection %q in this project", spec.Connection)
	}
	return binding, true, nil
}

func (s *Service) pinnedRepositoryConnection(ctx context.Context, projectID string, revision core.WorkflowSourceRevision) (core.RepositoryConnection, error) {
	binding, err := s.Store.GetRepositoryConnection(ctx, revision.RepositoryConnectionID)
	if err != nil {
		return binding, fmt.Errorf("source %s repository connection: %w", revision.Alias, err)
	}
	if binding.ProjectID != projectID || revision.CloneURL != binding.RepositoryURL || !matchesBoundRepository(binding, revision.Repository) ||
		binding.GitHubAppID != revision.GitHubAppID || binding.CredentialSecretID != revision.CredentialSecretID {
		return binding, fmt.Errorf("source %s repository connection changed; sync and run a new revision", revision.Alias)
	}
	return binding, nil
}

func (s *Service) boundSourceConfig(ctx context.Context, config core.ConfigSource, binding core.RepositoryConnection) (core.ConfigSource, error) {
	config.RepositoryProxyAppID = ""
	if (binding.GitHubAppID == "") == (binding.CredentialSecretID == "") {
		return config, fmt.Errorf("repository connection %q requires exactly one credential", binding.Name)
	}
	if binding.GitHubAppID != "" {
		app, err := s.Store.GetGitHubApp(ctx, binding.GitHubAppID)
		if err != nil {
			return config, err
		}
		if app.State != "ready" || app.InstallationID < 1 {
			return config, fmt.Errorf("GitHub App for connection %q must be installed and verified", binding.Name)
		}
		if err := githubapp.ValidateRepositoryHost(binding.RepositoryURL, app.WebURL); err != nil {
			return config, err
		}
	} else {
		secret, err := s.Store.GetSecret(ctx, binding.CredentialSecretID)
		if err != nil {
			return config, err
		}
		if secret.Type != core.SecretTypeGitHubToken {
			return config, fmt.Errorf("repository connection %q requires a GitHub token secret", binding.Name)
		}
	}
	if binding.CredentialSecretID != "" && config.GitHubAppID != "" {
		parent, err := s.Store.GetGitHubApp(ctx, config.GitHubAppID)
		if err != nil {
			return config, err
		}
		if githubapp.ValidateRepositoryHost(binding.RepositoryURL, parent.WebURL) == nil {
			config.RepositoryProxyAppID = parent.ID
		}
	}
	config.RepositoryConnectionID = binding.ID
	config.GitHubAppID, config.CredentialSecretID = binding.GitHubAppID, binding.CredentialSecretID
	// GitHub's numeric repository ID on the parent belongs to the config repository.
	config.Repository, config.RepositoryID = binding.RepositoryURL, 0
	return config, nil
}

func (s *Service) workflowSourceApp(ctx context.Context, configAppID, templateAppID, name string) (core.GitHubAppConnection, error) {
	if name == "" {
		if templateAppID == "" || templateAppID == configAppID {
			return core.GitHubAppConnection{}, nil
		}
		return s.Store.GetGitHubApp(ctx, templateAppID)
	}
	connections, err := s.Store.ListGitHubApps(ctx)
	if err != nil {
		return core.GitHubAppConnection{}, err
	}
	for _, connection := range connections {
		if connection.Name == name {
			if connection.State != "ready" || connection.InstallationID < 1 {
				return core.GitHubAppConnection{}, fmt.Errorf("GitHub App connection %q must be installed and verified", name)
			}
			return connection, nil
		}
	}
	return core.GitHubAppConnection{}, fmt.Errorf("GitHub App connection %q was not found", name)
}

func githubAppSourceURL(connection core.GitHubAppConnection, repository string) (string, error) {
	repository = strings.TrimSpace(repository)
	if !isCloneURL(repository) {
		repository = strings.TrimRight(connection.WebURL, "/") + "/" + strings.Trim(strings.TrimSuffix(repository, ".git"), "/") + ".git"
	}
	if err := githubapp.ValidateRepositoryHost(repository, connection.WebURL); err != nil {
		return "", fmt.Errorf("GitHub App %q: %w", connection.Name, err)
	}
	parsed, err := url.Parse(repository)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("GitHub App source URL must contain only the GitHub host and repository path")
	}
	owner, name, ok := strings.Cut(strings.Trim(parsed.Path, "/"), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") ||
		strings.ContainsAny(parsed.Path, "\\ \t\r\n") || strings.Contains(parsed.Path, "..") {
		return "", errors.New("GitHub App source repository must use owner/name")
	}
	return parsed.String(), nil
}

// The revision carries the resolved connection and host so later stage and
// worker checkouts can use the same binding even after an App is renamed.
func (s *Service) workflowSourceAccess(ctx context.Context, config core.ConfigSource, revision core.WorkflowSourceRevision) (core.ConfigSource, string, error) {
	if revision.RepositoryConnectionID != "" {
		binding, err := s.pinnedRepositoryConnection(ctx, config.ProjectID, revision)
		if err != nil {
			return config, "", err
		}
		config, err = s.boundSourceConfig(ctx, config, binding)
		return config, binding.RepositoryURL, err
	}
	if revision.CredentialSecretID != "" {
		return config, "", errors.New("pinned repository credential is missing its named connection")
	}
	if revision.GitHubAppID == "" {
		return config, revision.Repository, nil
	}
	connection, err := s.pinnedWorkflowSourceApp(ctx, revision)
	if err != nil {
		return config, "", err
	}
	config.GitHubAppID, config.CredentialSecretID = connection.ID, ""
	return config, revision.CloneURL, nil
}

func (s *Service) pinnedWorkflowSourceApp(ctx context.Context, revision core.WorkflowSourceRevision) (core.GitHubAppConnection, error) {
	connection, err := s.Store.GetGitHubApp(ctx, revision.GitHubAppID)
	if err != nil {
		return connection, fmt.Errorf("source %s GitHub App connection: %w", revision.Alias, err)
	}
	if err := githubapp.ValidateRepositoryHost(revision.CloneURL, connection.WebURL); err != nil {
		return connection, fmt.Errorf("source %s GitHub App host: %w", revision.Alias, err)
	}
	if normalizeRepository(revision.CloneURL) != normalizeRepository(revision.Repository) {
		return connection, fmt.Errorf("source %s clone URL no longer matches its repository", revision.Alias)
	}
	return connection, nil
}

func (s *Service) workflowRepositoryAccess(ctx context.Context, config core.ConfigSource, revision core.WorkflowSourceRevision) (repositoryAccess, error) {
	config, repository, err := s.workflowSourceAccess(ctx, config, revision)
	if err != nil {
		return repositoryAccess{}, err
	}
	return s.repositoryAccess(ctx, config, repository)
}

func (s *Service) workflowTemplateSourceApp(ctx context.Context, resource core.WorkflowResource) (string, error) {
	if !resource.Temporary {
		return "", nil
	}
	if trigger, ok := store.WorkflowPreviewTriggerFromContext(ctx); ok && (trigger.ResourceID == "" || trigger.ResourceID == resource.ID) {
		if trigger.TemplateSource != nil {
			return trigger.GitHubAppID, nil
		}
		return "", nil
	}
	triggers, err := s.Store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return "", err
	}
	for _, trigger := range triggers {
		if trigger.ResourceID == resource.ID && trigger.ClosedAt == nil && trigger.TemplateSource != nil {
			return trigger.GitHubAppID, nil
		}
	}
	return "", nil
}
