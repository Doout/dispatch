package deploy

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/repositoryproxy"
	"github.com/doout/dispatch/internal/runtimecontract"
)

const (
	SourceAuthGitHubToken = "github_token"
	SourceAuthGitHubApp   = "github_app"
	SourceAuthSSHKey      = "ssh_key"
)

type secretReader interface {
	repositoryConnectionReader
	GetSecret(context.Context, string) (core.Secret, error)
}

type githubAppTokenSource interface {
	RepositoryToken(context.Context, string, string) (string, error)
}

type SecretResolver interface {
	Resolve(context.Context, string) ([]byte, error)
}

// SourceAuthExecutor resolves one application-scoped source credential just
// before execution. Plaintext exists only in the in-memory App copy passed to
// the executor chain.
type SourceAuthExecutor struct {
	Next       Executor
	Secrets    secretReader
	Vault      *secretcrypto.Vault
	Resolver   SecretResolver
	GitHubApps githubAppTokenSource
	Proxy      *repositoryproxy.Service
}

func (e SourceAuthExecutor) Deploy(ctx context.Context, deployment core.Deployment, app core.App, server core.Server, progress Progress) error {
	if err := e.RuntimeCapabilities(app, server).Check(ctx, runtimecontract.Deploy); err != nil {
		return err
	}
	if app.HasInlineCompose() && strings.TrimSpace(app.PreDeployHook) == "" && strings.TrimSpace(app.PostDeployHook) == "" {
		return e.Next.Deploy(ctx, deployment, app, server, progress)
	}
	resolved, err := e.Resolve(ctx, app)
	if err != nil {
		return err
	}
	return e.Next.Deploy(ctx, deployment, resolved, server, progress)
}

func (e SourceAuthExecutor) Cleanup(ctx context.Context, app core.App, server core.Server, progress Progress) error {
	id, _ := ctx.Value(cleanupOperationKey{}).(string)
	capabilities := e.RuntimeCapabilities(app, server)
	if id == "" {
		if err := capabilities.Check(ctx, runtimecontract.Destroy); err != nil {
			return err
		}
	}
	cleaner, ok := e.Next.(CleanupExecutor)
	if !ok {
		return ErrCleanupUnsupported
	}
	// Enrolled Docker agents destroy resources by their ownership labels.
	if id != "" || capabilities.Driver == "docker-agent" {
		app.SourceCredential, app.SourceProxyURL, app.SourceNoProxy = "", "", ""
		return cleaner.Cleanup(ctx, app, server, progress)
	}
	resolved, err := e.Resolve(ctx, app)
	if err != nil {
		return err
	}
	return cleaner.Cleanup(ctx, resolved, server, progress)
}

// Resolve decrypts the application's scoped repository credential into a
// transient App copy. Callers must never persist or serialize the result.
func (e SourceAuthExecutor) Resolve(ctx context.Context, app core.App) (core.App, error) {
	if err := checkDeploymentRepositoryConnection(ctx, e.Secrets, app); err != nil {
		return app, err
	}
	if e.Proxy != nil && repositoryproxy.IsHTTPRepository(app.SourceRepo) {
		proxyContext := ctx
		if app.SourceAuthType == SourceAuthGitHubApp {
			proxyContext = repositoryproxy.WithGitHubApp(ctx, app.SourceCredentialID)
		} else if app.HelmProvenance.RepositoryConnectionID != "" {
			proxyContext = repositoryproxy.WithGitHubApp(ctx, app.HelmProvenance.RepositoryProxyAppID)
		}
		proxyURL, noProxy, err := e.Proxy.Resolve(proxyContext)
		if err != nil {
			return app, err
		}
		app.SourceProxyURL, app.SourceNoProxy = proxyURL, noProxy
	}
	if app.SourceCredentialID == "" {
		return app, nil
	}
	if app.SourceAuthType == SourceAuthGitHubApp {
		if e.GitHubApps == nil {
			return app, errors.New("GitHub App authentication is not configured")
		}
		token, err := e.GitHubApps.RepositoryToken(githubapp.WithRepositoryConnection(ctx, app.HelmProvenance.RepositoryConnectionID), app.SourceCredentialID, app.SourceRepo)
		if err != nil {
			return app, fmt.Errorf("create GitHub App installation token: %w", err)
		}
		app.SourceCredential = token
		return app, nil
	}
	if e.Secrets == nil || (e.Resolver == nil && e.Vault == nil) {
		return app, errors.New("source credential storage is not configured")
	}
	secret, err := e.Secrets.GetSecret(ctx, app.SourceCredentialID)
	if err != nil {
		return app, fmt.Errorf("load source credential: %w", err)
	}
	if app.HelmProvenance.RepositoryConnectionID != "" && secret.Type != core.SecretTypeGitHubToken {
		return app, errors.New("repository connection requires a GitHub token")
	}
	if err := ValidateSourceCredentialType(app.SourceAuthType, secret.Type); err != nil {
		return app, err
	}
	var plaintext []byte
	if e.Resolver != nil {
		plaintext, err = e.Resolver.Resolve(ctx, secret.ID)
	} else {
		plaintext, err = e.Vault.Decrypt("secret:"+secret.ID, secret.EncryptedValue)
	}
	if err != nil {
		return app, fmt.Errorf("resolve source credential: %w", err)
	}
	defer clear(plaintext)
	app.SourceCredential = string(plaintext)
	return app, nil
}

func ValidateSourceCredentialType(authType string, secretType core.SecretType) error {
	if secretType == "" {
		secretType = core.SecretTypeText
	}
	switch authType {
	case SourceAuthGitHubApp:
		return errors.New("GitHub App connections are not stored as secrets")
	case SourceAuthGitHubToken:
		if secretType == core.SecretTypeText || secretType == core.SecretTypeAPIToken || secretType == core.SecretTypeGitHubToken {
			return nil
		}
		return errors.New("the selected secret is not a GitHub token")
	case SourceAuthSSHKey:
		if secretType == core.SecretTypeText || secretType == core.SecretTypeSSHPrivateKey {
			return nil
		}
		return errors.New("the selected secret is not an SSH private key")
	default:
		return fmt.Errorf("unsupported source authentication type %q", authType)
	}
}

func PrepareGitEnvironment(app core.App) ([]string, func(), error) {
	environment, cleanup, err := prepareGitCredentials(app)
	if err != nil {
		return nil, cleanup, err
	}
	return repositoryproxy.GitEnvironment(environment, app.SourceRepo, app.SourceProxyURL, app.SourceNoProxy), cleanup, nil
}

func prepareGitCredentials(app core.App) ([]string, func(), error) {
	environment := os.Environ()
	environment = append(environment, "GIT_TERMINAL_PROMPT=0")
	credential := strings.TrimSpace(app.SourceCredential)
	if app.SourceAuthType == "" && credential == "" {
		credential = strings.TrimSpace(os.Getenv("DISPATCH_GIT_TOKEN"))
		if credential != "" {
			environment = append(environment,
				"GIT_CONFIG_COUNT=1",
				"GIT_CONFIG_KEY_0=http.extraHeader",
				"GIT_CONFIG_VALUE_0=Authorization: Bearer "+credential,
			)
		}
		return environment, func() {}, nil
	}
	if credential == "" {
		return nil, func() {}, errors.New("configured source credential is empty")
	}
	switch app.SourceAuthType {
	case SourceAuthGitHubApp, SourceAuthGitHubToken:
		key, header, bound, err := gitAuthorization(app, credential)
		if err != nil {
			return nil, func() {}, err
		}
		environment = append(environment, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0="+key, "GIT_CONFIG_VALUE_0="+header)
		if bound {
			environment = append(environment, "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_1=http.followRedirects", "GIT_CONFIG_VALUE_1=false")
		}
		return environment, func() {}, nil
	case SourceAuthSSHKey:
		directory, err := os.MkdirTemp("", "dispatch-git-auth-")
		if err != nil {
			return nil, func() {}, err
		}
		keyPath := filepath.Join(directory, "identity")
		knownHostsPath := filepath.Join(directory, "known_hosts")
		if err := os.WriteFile(keyPath, []byte(credential+"\n"), 0o600); err != nil {
			_ = os.RemoveAll(directory)
			return nil, func() {}, fmt.Errorf("materialize SSH private key: %w", err)
		}
		environment = append(environment, "GIT_SSH_COMMAND=ssh -i "+keyPath+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="+knownHostsPath)
		return environment, func() { _ = os.RemoveAll(directory) }, nil
	default:
		return nil, func() {}, fmt.Errorf("unsupported source authentication type %q", app.SourceAuthType)
	}
}

// Named connections authorize exactly one HTTPS repository. Scope the header to
// its Git URL and disable redirects so the credential cannot follow a move.
func gitAuthorization(app core.App, credential string) (key, header string, bound bool, err error) {
	bound = app.RepositoryConnectionID != "" || app.HelmProvenance.RepositoryConnectionID != ""
	key = "http.extraHeader"
	if bound {
		repository, err := core.CanonicalRepositoryURL(app.SourceRepo)
		if err != nil || repository != app.SourceRepo {
			return "", "", true, errors.New("named repository authentication requires its bound HTTPS clone URL")
		}
		key = "http." + repository + ".extraHeader"
	}
	if bound || app.SourceAuthType == SourceAuthGitHubApp {
		return key, "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+credential)), bound, nil
	}
	return key, "Authorization: Bearer " + credential, false, nil
}

func runGitForApp(ctx context.Context, app core.App, args ...string) error {
	environment, cleanup, err := PrepareGitEnvironment(app)
	if err != nil {
		return err
	}
	defer cleanup()
	var output strings.Builder
	cmd := commandWithEnvironment(ctx, environment, nil, &output, "git", args...)
	if cmd != nil {
		detail := strings.TrimSpace(repositoryproxy.RedactGitOutput(output.String(), environment))
		if detail != "" {
			return fmt.Errorf("%w: %s", cmd, detail)
		}
	}
	return cmd
}
