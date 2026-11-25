package deploy

import (
	"context"
	"errors"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
)

type repositoryConnectionReader interface {
	GetRepositoryConnection(context.Context, string) (core.RepositoryConnection, error)
	GetGitHubApp(context.Context, string) (core.GitHubAppConnection, error)
}

// Generated Helm apps can outlive the credential selected for their revision.
func checkDeploymentRepositoryConnection(ctx context.Context, data repositoryConnectionReader, app core.App) error {
	id := app.HelmProvenance.RepositoryConnectionID
	if id == "" {
		return nil
	}
	if data == nil {
		return errors.New("repository connection storage is unavailable")
	}
	binding, err := data.GetRepositoryConnection(ctx, id)
	if err != nil {
		return err
	}
	target, err := core.CanonicalRepositoryURL(app.SourceRepo)
	if err != nil || target != binding.RepositoryURL || app.ProjectID != binding.ProjectID {
		return errors.New("deployment repository no longer matches its named connection")
	}
	if binding.GitHubAppID != "" {
		if app.SourceAuthType != SourceAuthGitHubApp || binding.GitHubAppID != app.SourceCredentialID {
			return errors.New("repository connection changed; run the workflow again")
		}
	} else if app.SourceAuthType != SourceAuthGitHubToken || app.SourceCredentialID == "" || app.SourceCredentialID != binding.CredentialSecretID {
		return errors.New("repository connection changed; run the workflow again")
	}
	appID := binding.GitHubAppID
	if appID == "" {
		appID = app.HelmProvenance.RepositoryProxyAppID
	}
	if appID != "" {
		connection, err := data.GetGitHubApp(ctx, appID)
		if err != nil {
			return err
		}
		if binding.GitHubAppID != "" && (connection.State != "ready" || connection.InstallationID == 0) {
			return errors.New("the repository connection's GitHub App must be installed and verified")
		}
		return githubapp.ValidateRepositoryHost(target, connection.WebURL)
	}
	return nil
}
