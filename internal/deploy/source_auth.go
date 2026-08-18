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
)

const (
	SourceAuthGitHubToken = "github_token"
	SourceAuthGitHubApp   = "github_app"
	SourceAuthSSHKey      = "ssh_key"
)

type secretReader interface {
	GetSecret(context.Context, string) (core.Secret, error)
}

type githubAppTokenSource interface {
	InstallationToken(context.Context, string) (string, error)
}

// SourceAuthExecutor resolves one application-scoped source credential just
// before execution. Plaintext exists only in the in-memory App copy passed to
// the executor chain.
type SourceAuthExecutor struct {
	Next       Executor
	Secrets    secretReader
	Vault      *secretcrypto.Vault
	GitHubApps githubAppTokenSource
}

func (e SourceAuthExecutor) Deploy(ctx context.Context, deployment core.Deployment, app core.App, server core.Server, progress Progress) error {
	resolved, err := e.Resolve(ctx, app)
	if err != nil {
		return err
	}
	return e.Next.Deploy(ctx, deployment, resolved, server, progress)
}

func (e SourceAuthExecutor) Cleanup(ctx context.Context, app core.App, server core.Server, progress Progress) error {
	cleaner, ok := e.Next.(CleanupExecutor)
	if !ok {
		return ErrCleanupUnsupported
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
	if app.SourceCredentialID == "" {
		return app, nil
	}
	if app.SourceAuthType == SourceAuthGitHubApp {
		if e.GitHubApps == nil {
			return app, errors.New("GitHub App authentication is not configured")
		}
		token, err := e.GitHubApps.InstallationToken(ctx, app.SourceCredentialID)
		if err != nil {
			return app, fmt.Errorf("create GitHub App installation token: %w", err)
		}
		app.SourceCredential = token
		return app, nil
	}
	if e.Secrets == nil || e.Vault == nil {
		return app, errors.New("source credential storage is not configured")
	}
	secret, err := e.Secrets.GetSecret(ctx, app.SourceCredentialID)
	if err != nil {
		return app, fmt.Errorf("load source credential: %w", err)
	}
	if err := ValidateSourceCredentialType(app.SourceAuthType, secret.Type); err != nil {
		return app, err
	}
	plaintext, err := e.Vault.Decrypt("secret:"+secret.ID, secret.EncryptedValue)
	if err != nil {
		return app, fmt.Errorf("decrypt source credential: %w", err)
	}
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

func prepareGitEnvironment(app core.App) ([]string, func(), error) {
	environment := append([]string{}, os.Environ()...)
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
	case SourceAuthGitHubApp:
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + credential))
		environment = append(environment,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic,
		)
		return environment, func() {}, nil
	case SourceAuthGitHubToken:
		environment = append(environment,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Bearer "+credential,
		)
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

func runGitForApp(ctx context.Context, app core.App, args ...string) error {
	environment, cleanup, err := prepareGitEnvironment(app)
	if err != nil {
		return err
	}
	defer cleanup()
	var output strings.Builder
	cmd := commandWithEnvironment(ctx, environment, nil, &output, "git", args...)
	if cmd != nil {
		detail := strings.TrimSpace(output.String())
		if detail != "" {
			return fmt.Errorf("%w: %s", cmd, detail)
		}
	}
	return cmd
}
