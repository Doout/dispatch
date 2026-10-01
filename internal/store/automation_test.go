package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestAutomationPersistence(t *testing.T) {
	testAutomationPersistence(t, filepath.Join(t.TempDir(), "automation.db"))
}
func TestAutomationPostgres(t *testing.T) {
	dsn := os.Getenv("DISPATCH_AUTOMATION_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_AUTOMATION_POSTGRES_URL to a disposable database")
	}
	testAutomationPersistence(t, dsn)
}
func testAutomationPersistence(t *testing.T, dsn string) {
	ctx := context.Background()
	data, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	project := core.Project{ID: "automation-project", Name: "Automation", CreatedAt: now}
	if err = data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	account := core.ServiceAccount{ID: "automation-account", Name: "deploy-agent", State: "active", CreatedAt: now, UpdatedAt: now}
	if err = data.CreateServiceAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	c := core.AutomationCredential{ID: "credential-original", AccountID: account.ID, Name: "pipeline", TokenHash: "original-digest", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err = data.IssueAutomationCredential(ctx, c, ""); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(time.Hour), now.Add(time.Hour + time.Millisecond), now.Add(2 * time.Hour)} {
		if _, _, err = data.AuthenticateAutomationCredential(ctx, c.TokenHash, at); !errors.Is(err, ErrAutomationCredential) {
			t.Fatal("expired credential authenticated", err)
		}
	}
	principal, credential, err := data.AuthenticateAutomationCredential(ctx, c.TokenHash, now.Add(time.Second))
	if err != nil || principal.ID != account.ID || credential.TokenHash != "" || credential.LastUsedAt == nil {
		t.Fatal("authentication metadata", principal, credential, err)
	}
	account.State = "disabled"
	if err = data.UpdateServiceAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if _, _, err = data.AuthenticateAutomationCredential(ctx, c.TokenHash, now); !errors.Is(err, ErrAutomationCredential) {
		t.Fatal("disabled account authenticated", err)
	}
	account.State = "active"
	if err = data.UpdateServiceAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	// Competing rotations can retire the old credential exactly once. The losing
	// transaction must not leave another live credential behind.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, suffix := range []string{"a", "b"} {
		wg.Add(1)
		go func(suffix string) {
			defer wg.Done()
			next := c
			next.ID = "credential-" + suffix
			next.TokenHash = "digest-" + suffix
			results <- data.IssueAutomationCredential(ctx, next, c.ID)
		}(suffix)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("rotation success count %d", success)
	}
	if _, _, err = data.AuthenticateAutomationCredential(ctx, c.TokenHash, now); !errors.Is(err, ErrAutomationCredential) {
		t.Fatal("retired credential authenticated", err)
	}
	credentials, err := data.ListAutomationCredentials(ctx, account.ID)
	if err != nil || len(credentials) != 2 {
		t.Fatal(credentials, err)
	}
	var active core.AutomationCredential
	for _, v := range credentials {
		if v.TokenHash != "" {
			t.Fatal("hash exposed")
		}
		if v.RevokedAt == nil {
			active = v
		}
	}
	if err = data.RevokeAutomationCredential(ctx, account.ID, active.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err = data.AuthenticateAutomationCredential(ctx, "digest-"+strings.TrimPrefix(active.ID, "credential-"), now); !errors.Is(err, ErrAutomationCredential) {
		t.Fatal("revoked credential authenticated", err)
	}
	expires := now.Add(time.Hour)
	grant := core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: project.ID, Permissions: []core.Permission{core.PermissionProjectView, core.PermissionInfrastructureCreate}, ExpiresAt: &expires, UpdatedAt: now}
	if err = data.SavePrincipalGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	grants, err := data.ListPrincipalGrants(ctx, core.PrincipalServiceAccount, account.ID)
	if err != nil || len(grants) != 1 || len(grants[0].Permissions) != 2 || grants[0].ExpiresAt == nil {
		t.Fatal(grants, err)
	}
	target := core.Server{ID: "automation-server", Name: "Owned", ProjectID: project.ID, Address: "host.example", Runtime: "docker", CreatedAt: now}
	if err = data.CreateServer(ctx, target); err != nil {
		t.Fatal(err)
	}
	target.ProjectID = "another-project"
	if err = data.UpdateServer(ctx, target); err == nil {
		t.Fatal("ownership changed")
	}
	saved, err := data.GetServer(ctx, target.ID)
	if err != nil || saved.ProjectID != project.ID {
		t.Fatal(saved, err)
	}
	assignment := core.InfrastructureAssignment{ProjectID: project.ID, Kind: "target", ResourceID: target.ID, UpdatedAt: now}
	if err = data.SaveInfrastructureAssignment(ctx, assignment); err != nil {
		t.Fatal(err)
	}
	assignments, err := data.ListInfrastructureAssignments(ctx, project.ID)
	if err != nil || len(assignments) != 1 || assignments[0].ResourceID != target.ID {
		t.Fatal(assignments, err)
	}
	event := core.AuditEvent{ID: "automation-audit", ActorID: account.ID, ActorType: core.PrincipalServiceAccount, CredentialID: active.ID, ProjectID: project.ID, Action: "deploy", Outcome: "succeeded", CreatedAt: now}
	if err = data.AppendAuditEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	events, err := data.ListAuditEvents(ctx, core.AuditFilter{ActorID: account.ID})
	if err != nil || len(events) != 1 || events[0].CredentialID != active.ID || events[0].ActorType != core.PrincipalServiceAccount {
		t.Fatal(events, err)
	}
	raw, _ := json.Marshal(credentials)
	if strings.Contains(string(raw), "digest") {
		t.Fatal("credential hash serialized")
	}
	if err = data.DeletePrincipalGrant(ctx, core.PrincipalServiceAccount, account.ID, project.ID); err != nil {
		t.Fatal(err)
	}
	grants, err = data.ListPrincipalGrants(ctx, core.PrincipalServiceAccount, account.ID)
	if err != nil || len(grants) != 0 {
		t.Fatal(grants, err)
	}
}
