package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestApplicationMutationAcceptanceSQLite(t *testing.T) {
	testApplicationMutationAcceptance(t, filepath.Join(t.TempDir(), "acceptance.db"))
}

func TestApplicationMutationAcceptancePostgres(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_STORE_POSTGRES_URL to a disposable PostgreSQL database")
	}
	testApplicationMutationAcceptance(t, dsn)
}

func testApplicationMutationAcceptance(t *testing.T, dsn string) {
	ctx := context.Background()
	s := mutationStore(t, dsn)
	now := time.Now().UTC()
	project := core.Project{ID: "acceptance-project", Name: "Acceptance", CreatedAt: now}
	server := core.Server{ID: "acceptance-target", Name: "Target", Runtime: core.ServerRuntimeDocker, CreatedAt: now}
	for _, err := range []error{s.CreateProject(ctx, project), s.CreateServer(ctx, server)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	receipt := mutationFixture("app-create")
	receipt.ProjectID, receipt.OperationKind, receipt.Action = project.ID, "application", "application.create"
	reserved, claimed, err := s.ReserveMutationReceipt(ctx, receipt, now)
	if err != nil || !claimed {
		t.Fatal("reserve application", err)
	}
	app := core.App{ID: reserved.OperationID, ProjectID: project.ID, ServerID: server.ID, Name: "Created once", BuildType: core.BuildTypeDockerfile, CreatedAt: now}
	claim := mutationClaim(reserved)
	stale := claim
	stale.ClaimToken = "replaced"
	if err = s.CreateApp(core.WithMutationAcceptance(ctx, stale), app); !errors.Is(err, ErrMutationClaimLost) {
		t.Fatal("stale application preparer accepted", err)
	}
	if _, err = s.GetApp(ctx, app.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("stale claim left an application", err)
	}
	if err = s.CreateApp(core.WithMutationAcceptance(ctx, claim), app); err != nil {
		t.Fatal(err)
	}
	// A lost response and store reopen keep the accepted app identity and outcome.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = mutationStore(t, dsn)
	saved, claimed, err := s.ReserveMutationReceipt(ctx, receipt, now.Add(time.Second))
	if err != nil || claimed || saved.State != "succeeded" || saved.OperationID != app.ID {
		t.Fatal("application acceptance repeated after restart", saved.State, claimed, err)
	}
	source := core.Deployment{ID: "acceptance-source", AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: now}
	if err = s.CreateDeployment(ctx, source); err != nil {
		t.Fatal(err)
	}
	rollbackReceipt := mutationFixture("rollback")
	rollbackReceipt.ProjectID, rollbackReceipt.OperationKind, rollbackReceipt.Action = project.ID, "deployment", "deployment.rollback"
	reserved, claimed, err = s.ReserveMutationReceipt(ctx, rollbackReceipt, now)
	if err != nil || !claimed {
		t.Fatal("reserve rollback", err)
	}
	d := core.Deployment{ID: reserved.OperationID, AppID: app.ID, State: core.DeploymentQueued, CreatedAt: now.Add(time.Second), RollbackCurrentID: source.ID}
	action := core.ReleaseAction{ID: "acceptance-action", ProjectID: project.ID, AppID: app.ID, DeploymentID: d.ID, SourceDeploymentID: source.ID, Action: "deployment.rollback", CreatedAt: now}
	claim = mutationClaim(reserved)
	stale = claim
	stale.ClaimToken = "replaced"
	if err = s.CreateRollbackDeployment(core.WithMutationAcceptance(ctx, stale), d, source, action); !errors.Is(err, ErrMutationClaimLost) {
		t.Fatal("stale rollback preparer accepted", err)
	}
	if _, err = s.GetDeployment(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("stale claim left a rollback", err)
	}
	if err = s.CreateRollbackDeployment(core.WithMutationAcceptance(ctx, claim), d, source, action); err != nil {
		t.Fatal(err)
	}
	saved, claimed, err = s.ReserveMutationReceipt(ctx, rollbackReceipt, now.Add(time.Second))
	if err != nil || claimed || saved.State != "accepted" || saved.OperationID != d.ID {
		t.Fatal("rollback acceptance repeated", saved.State, claimed, err)
	}
}
