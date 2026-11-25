package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestRepositoryConnectionNameClaims(t *testing.T) {
	testRepositoryConnectionNameClaims(t, filepath.Join(t.TempDir(), "connections.db"))
}

func TestRepositoryConnectionNameClaimsPostgres(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_STORE_POSTGRES_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	testRepositoryConnectionNameClaims(t, dsn)
}

func testRepositoryConnectionNameClaims(t *testing.T, dsn string) {
	ctx := t.Context()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Migrate(ctx))
	now := time.Now().UTC()
	must(s.CreateProject(ctx, core.Project{ID: "project", Name: "Project", CreatedAt: now}))
	must(s.CreateSecret(ctx, core.Secret{ID: "token", Name: "Token", Type: core.SecretTypeGitHubToken, Source: core.SecretSourceLocal, EnvironmentVariable: "TOKEN", EncryptedValue: "fixture", CreatedAt: now, UpdatedAt: now}))
	binding := core.RepositoryConnection{ID: "binding", Name: "gitops-read", ProjectID: "project", RepositoryURL: "https://example.test/org/repo.git", CredentialSecretID: "token", UpdatedAt: now}
	app := core.GitHubAppConnection{ID: "app", Name: binding.Name, AppID: 1, WebURL: "https://example.test", APIURL: "https://example.test/api/v3", CreatedAt: now, UpdatedAt: now}
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	go func() { <-start; outcomes <- s.CreateRepositoryConnection(ctx, binding) }()
	go func() { <-start; outcomes <- s.CreateGitHubApp(ctx, app) }()
	close(start)
	first, second := <-outcomes, <-outcomes
	if first != nil && second != nil || first == nil && second == nil || first != nil && !errors.Is(first, ErrAlreadyExists) || second != nil && !errors.Is(second, ErrAlreadyExists) {
		t.Fatalf("expected one successful name claim and one conflict, got %v / %v", first, second)
	}
}
