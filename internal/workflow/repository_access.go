package workflow

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/githubapp"
)

type repositoryAccess struct {
	url          string
	credentialID string
	environment  []string
	cleanup      func()
}

var commitRefPattern = regexp.MustCompile(`^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$`)

func (s *Service) repositoryHead(ctx context.Context, source core.ConfigSource, repository, branch string) (string, error) {
	if commitRefPattern.MatchString(branch) {
		return strings.ToLower(branch), nil
	}
	if source.GitHubAppID != "" {
		if s.GitHub == nil {
			return "", errors.New("GitHub App access is not configured")
		}
		return s.GitHub.RepositoryHead(ctx, source.GitHubAppID, repository, branch)
	}
	access, err := s.repositoryAccess(ctx, source, repository)
	if err != nil {
		return "", err
	}
	defer access.cleanup()
	if strings.ContainsAny(branch, " \t\r\n") {
		return "", errors.New("branch contains whitespace")
	}
	refs := []string{branch}
	if !strings.HasPrefix(branch, "refs/") {
		refs = []string{"refs/heads/" + branch, "refs/tags/" + branch}
	}
	args := []string{"ls-remote", "--exit-code", access.url}
	for _, ref := range refs {
		args = append(args, ref, ref+"^{}")
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = access.environment
	var output limitedBuffer
	output.limit = maxJobLogBytes
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("resolve repository branch: %w: %s", err, strings.TrimSpace(output.String()))
	}
	for _, ref := range refs {
		if sha, err := parseRepositoryHead(output.String(), ref+"^{}"); err == nil {
			return sha, nil
		}
		if sha, err := parseRepositoryHead(output.String(), ref); err == nil {
			return sha, nil
		}
	}
	return "", errors.New("repository returned no matching ref")
}

func parseRepositoryHead(output, ref string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != ref {
			continue
		}
		if len(fields[0]) != 40 && len(fields[0]) != 64 {
			break
		}
		if _, err := hex.DecodeString(fields[0]); err == nil {
			return fields[0], nil
		}
		break
	}
	return "", errors.New("repository returned an invalid commit")
}

func (s *Service) repositoryFiles(ctx context.Context, source core.ConfigSource, revision string) ([]githubapp.RepositoryFile, error) {
	if source.GitHubAppID != "" {
		return s.GitHub.RepositoryFiles(ctx, source.GitHubAppID, source.Repository, revision, source.Path)
	}
	access, err := s.repositoryAccess(ctx, source, source.Repository)
	if err != nil {
		return nil, err
	}
	defer access.cleanup()
	root, err := os.MkdirTemp("", "dispatch-config-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	cache := s.Repositories
	if cache == nil {
		cache = newRepositoryCache("")
		s.Repositories = cache
	}
	worktree, err := cache.checkout(ctx, access.url, access.credentialID, source.Branch, revision, filepath.Join(root, "repository"), access.environment)
	if err != nil {
		return nil, err
	}
	defer worktree.remove(context.Background())
	configurationRoot, err := safeJoin(worktree.path, source.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(configurationRoot)
	if err != nil {
		return nil, fmt.Errorf("configuration path %s was not found", source.Path)
	}
	paths := []string{}
	if !info.IsDir() {
		paths = append(paths, configurationRoot)
	} else if err := filepath.WalkDir(configurationRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && isConfigurationFile(path) {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no YAML or JSON configuration files found at %s", source.Path)
	}
	if len(paths) > 64 {
		return nil, errors.New("configuration path contains more than 64 YAML or JSON files")
	}
	items := make([]githubapp.RepositoryFile, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.Size() > 1<<20 {
			return nil, fmt.Errorf("configuration file %s exceeds 1 MiB", path)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(worktree.path, path)
		if err != nil {
			return nil, err
		}
		items = append(items, githubapp.RepositoryFile{Path: filepath.ToSlash(relative), Contents: contents})
	}
	return items, nil
}

func (s *Service) repositoryAccess(ctx context.Context, source core.ConfigSource, repository string) (repositoryAccess, error) {
	repositoryURL, err := s.repositoryCloneURL(ctx, source, repository)
	if err != nil {
		return repositoryAccess{}, err
	}
	if source.GitHubAppID != "" {
		token, err := s.GitHub.InstallationToken(ctx, source.GitHubAppID)
		if err != nil {
			return repositoryAccess{}, err
		}
		environment, cleanup, err := deploy.PrepareGitEnvironment(core.App{SourceAuthType: deploy.SourceAuthGitHubApp, SourceCredential: token})
		return repositoryAccess{url: repositoryURL, credentialID: "github-app:" + source.GitHubAppID, environment: environment, cleanup: cleanup}, err
	}
	if source.CredentialSecretID == "" || s.Secrets == nil {
		return repositoryAccess{}, errors.New("repository credential resolution is not configured")
	}
	secret, err := s.Store.GetSecret(ctx, source.CredentialSecretID)
	if err != nil {
		return repositoryAccess{}, err
	}
	credential, err := s.Secrets.Resolve(ctx, secret.ID)
	if err != nil {
		return repositoryAccess{}, err
	}
	defer clear(credential)
	authType := deploy.SourceAuthGitHubToken
	if secret.Type == core.SecretTypeSSHPrivateKey {
		authType = deploy.SourceAuthSSHKey
	}
	if err := deploy.ValidateSourceCredentialType(authType, secret.Type); err != nil {
		return repositoryAccess{}, err
	}
	environment, cleanup, err := deploy.PrepareGitEnvironment(core.App{SourceAuthType: authType, SourceCredential: string(credential)})
	return repositoryAccess{url: repositoryURL, credentialID: "secret:" + secret.ID, environment: environment, cleanup: cleanup}, err
}

func (s *Service) repositoryCloneURL(ctx context.Context, source core.ConfigSource, repository string) (string, error) {
	repository = strings.TrimSpace(repository)
	if isCloneURL(repository) {
		return repository, nil
	}
	if source.GitHubAppID != "" {
		connection, err := s.Store.GetGitHubApp(ctx, source.GitHubAppID)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(connection.WebURL, "/") + "/" + strings.Trim(strings.TrimSuffix(repository, ".git"), "/") + ".git", nil
	}
	base := strings.TrimSpace(source.Repository)
	if marker := strings.Index(base, ":"); marker > 0 && strings.Contains(base[:marker], "@") {
		return base[:marker+1] + strings.Trim(strings.TrimSuffix(repository, ".git"), "/") + ".git", nil
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return "", errors.New("repository URL cannot be derived from the configuration source")
	}
	return parsed.Scheme + "://" + parsed.Host + "/" + strings.Trim(strings.TrimSuffix(repository, ".git"), "/") + ".git", nil
}

func isCloneURL(value string) bool {
	if strings.Contains(value, "://") {
		return true
	}
	marker := strings.Index(value, ":")
	return marker > 0 && strings.Contains(value[:marker], "@")
}

func isConfigurationFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml", ".json":
		return true
	default:
		return false
	}
}
