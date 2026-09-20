package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestOperationsDashboardSQLite(t *testing.T) {
	testOperationsDashboard(t, filepath.Join(t.TempDir(), "ops.db"))
}
func TestOperationsDashboardPostgres(t *testing.T) {
	dsn := os.Getenv("DISPATCH_OPERATIONS_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_OPERATIONS_POSTGRES_URL to a disposable database")
	}
	testOperationsDashboard(t, dsn)
}
func testOperationsDashboard(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
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
	prefix := fmt.Sprintf("opsdash-%d", time.Now().UnixNano())
	project, hidden, server := prefix+"-project", prefix+"-hidden", prefix+"-server"
	now := time.Date(2030, 1, 2, 0, 0, 0, 100, time.UTC)
	since := now.Add(-24 * time.Hour)
	must(s.CreateProject(ctx, core.Project{ID: project, Name: prefix + " Public project", CreatedAt: now}))
	must(s.CreateProject(ctx, core.Project{ID: hidden, Name: prefix + " Secret project", CreatedAt: now}))
	must(s.CreateServer(ctx, core.Server{ID: server, Name: server, Runtime: "docker", CreatedAt: now}))
	person := core.User{ID: prefix + "-user", Username: prefix + "-username", DisplayName: "Alice 100%", SystemRole: "member", State: "active", CreatedAt: now, UpdatedAt: now}
	must(s.CreateUser(ctx, person))
	team := core.Team{ID: prefix + "-team", Name: "Platform", CreatedAt: now, UpdatedAt: now}
	must(s.CreateTeam(ctx, team))
	for i := 0; i < 105; i++ {
		app := core.App{ID: fmt.Sprintf("%s-app-%03d", prefix, i), ProjectID: project, Name: fmt.Sprintf("Application %03d", i), ServerID: server, BuildType: core.BuildTypeDockerfile, Generated: i == 1, CreatedAt: now}
		if i == 2 {
			app.Name = "aPPLICATION 000"
		}
		must(s.CreateApp(ctx, app))
		if i == 0 {
			must(s.SaveApplicationOwner(ctx, core.ApplicationOwner{AppID: app.ID, PrincipalType: "user", PrincipalID: person.ID, UpdatedAt: now}))
		}
		if i == 1 {
			must(s.SaveApplicationOwner(ctx, core.ApplicationOwner{AppID: app.ID, PrincipalType: "team", PrincipalID: team.ID, UpdatedAt: now}))
		}
	}
	for _, app := range []core.App{{ID: prefix + "-closed", Name: "Closed", State: "closed"}, {ID: prefix + "-template", Name: "Template", Template: true}, {ID: prefix + "-private", Name: "Secret app", ProjectID: hidden}} {
		if app.ProjectID == "" {
			app.ProjectID = project
		}
		app.ServerID = server
		app.BuildType = "dockerfile"
		app.CreatedAt = now
		must(s.CreateApp(ctx, app))
	}
	firstApp := fmt.Sprintf("%s-app-%03d", prefix, 0)
	// Hundreds of later non-matching records must not hide older matching search hits.
	for i := 0; i < 120; i++ {
		must(s.AppendAuditEvent(ctx, core.AuditEvent{ID: fmt.Sprintf("%s-z-%03d", prefix, i), ProjectID: project, ActorID: "bob", Action: "PUT /unrelated", Outcome: "succeeded", CreatedAt: now.Add(-time.Hour)}))
	}
	events := []core.AuditEvent{
		{ID: prefix + "-a-start", ProjectID: project, AppID: firstApp, ActorName: "Alice 100%", Action: "PUT /api/v1/apps/{id}/owner", Outcome: "rejected", CreatedAt: since},
		{ID: prefix + "-b-fraction", ProjectID: project, AppID: firstApp, ActorName: "Alice 100%", Action: "PUT /api/v1/apps/{id}/owner", Outcome: "rejected", CreatedAt: since.Add(time.Nanosecond)},
		{ID: prefix + "-old", ProjectID: project, ActorName: "Old", Action: "PUT /old", Outcome: "rejected", CreatedAt: since.Add(-time.Nanosecond)},
		{ID: prefix + "-end", ProjectID: project, ActorName: "Future", Action: "PUT /end", Outcome: "rejected", CreatedAt: now},
		{ID: prefix + "-hidden", ProjectID: hidden, ActorName: "Private actor", Action: "PUT /private", Outcome: "rejected", CreatedAt: now.Add(-time.Minute)},
		{ID: prefix + "-global", ActorName: "Controller", Action: "PUT /global", Outcome: "rejected", CreatedAt: now.Add(-time.Minute)},
	}
	for _, e := range events {
		must(s.AppendAuditEvent(ctx, e))
	}
	f := core.AuditFilter{ProjectIDs: []string{project}, Query: "alice 100%", Outcome: "rejected", Since: &since, Until: &now, Limit: 1}
	page, err := s.ListAuditEvents(ctx, f)
	must(err)
	if len(page) != 1 || page[0].ID != prefix+"-b-fraction" {
		t.Fatal("audit filters were applied after pagination")
	}
	f.Before = page[0].ID
	page, err = s.ListAuditEvents(ctx, f)
	must(err)
	if len(page) != 1 || page[0].ID != prefix+"-a-start" {
		t.Fatal("audit filtered cursor lost matching record")
	}
	f.Before = ""
	f.Query = "100_"
	page, err = s.ListAuditEvents(ctx, f)
	must(err)
	if len(page) != 0 {
		t.Fatal("audit search interpreted SQL wildcard")
	}
	f.Query = "Application 000"
	page, err = s.ListAuditEvents(ctx, f)
	must(err)
	if len(page) != 1 {
		t.Fatal("audit current application-name search failed")
	}
	changes := s.Changes()
	summary, err := s.GetOperationsSummary(ctx, []string{project}, false, now)
	must(err)
	select {
	case <-changes:
		t.Fatal("read-only summary broadcast a store change")
	default:
	}
	if summary.Audit.Total != 122 || summary.Audit.Rejected != 2 || len(summary.Audit.Recent) != 5 || summary.Ownership.Total != 105 || summary.Ownership.Unassigned != 103 || summary.Backups != nil {
		t.Fatalf("wrong scoped aggregate %+v", summary)
	}
	for _, e := range summary.Audit.Recent {
		if e.ProjectID != project || e.CreatedAt.Before(since) || !e.CreatedAt.Before(now) {
			t.Fatal("recent audit escaped scope/range")
		}
	}
	empty, err := s.GetOperationsSummary(ctx, []string{}, false, now)
	must(err)
	if empty.Audit.Total != 0 || empty.Ownership.Total != 0 {
		t.Fatal("empty grants widened summary scope")
	}
	filter := core.OwnershipFilter{ProjectIDs: []string{project}, Limit: 50}
	seen := map[string]bool{}
	lastName, lastID := "", ""
	for {
		rows, err := s.ListOperationsOwnership(ctx, filter)
		must(err)
		if len(rows) == 0 {
			break
		}
		for _, item := range rows {
			if item.ProjectID != project || seen[item.AppID] {
				t.Fatal("ownership pagination leaked or duplicated")
			}
			name := strings.ToLower(item.AppName)
			if name < lastName || (name == lastName && item.AppID <= lastID) {
				t.Fatal("unstable ownership ordering")
			}
			lastName, lastID = name, item.AppID
			seen[item.AppID] = true
		}
		last := rows[len(rows)-1]
		filter.BeforeName, filter.BeforeID = last.AppName, last.AppID
	}
	if len(seen) != 105 {
		t.Fatal("ownership omitted generated active application")
	}
	owners, err := s.ListOperationsOwnership(ctx, core.OwnershipFilter{ProjectIDs: []string{project}, Query: "alice 100%"})
	must(err)
	if len(owners) != 1 || owners[0].Owner == nil || owners[0].Owner.DisplayName != person.DisplayName {
		t.Fatal("joined owner search/display failed")
	}
	owners, err = s.ListOperationsOwnership(ctx, core.OwnershipFilter{ProjectIDs: []string{project}, Unassigned: true, Limit: 101})
	must(err)
	if len(owners) != 101 {
		t.Fatal("unassigned filter not applied before limit")
	}
	for _, o := range owners {
		if o.Owner != nil {
			t.Fatal("assigned app passed unassigned filter")
		}
	}
	assigned, err := s.ListOperationsOwnership(ctx, core.OwnershipFilter{ProjectIDs: []string{project}, Assigned: true})
	must(err)
	if len(assigned) != 2 {
		t.Fatal("assigned filter did not exclude unassigned applications")
	}
	for _, item := range assigned {
		if item.Owner == nil {
			t.Fatal("assigned filter returned missing owner")
		}
	}
	// Moving an application cannot reveal the new project's name through old audit.
	moved, err := s.GetApp(ctx, firstApp)
	must(err)
	moved.ProjectID = hidden
	moved.Name = "Hidden renamed application"
	must(s.UpdateApp(ctx, moved))
	f.Query = moved.Name
	f.Limit = 100
	page, err = s.ListAuditEvents(ctx, f)
	must(err)
	if len(page) != 0 {
		t.Fatal("audit search became a cross-project name oracle")
	}
	// Backup counts and latest restore checks must include records beyond the old100 cap.
	baseline, err := s.GetOperationsSummary(ctx, nil, true, now)
	must(err)
	verified := now.Add(2 * time.Hour)
	for i := 0; i < 105; i++ {
		b := core.BackupRecord{ID: fmt.Sprintf("%s-backup-%03d", prefix, i), Engine: "sqlite", State: "ready", Bytes: 123, CreatedAt: now.Add(time.Duration(i) * time.Minute)}
		if i == 0 {
			b.State = "verified"
			b.VerifiedAt = &verified
		}
		must(s.SaveBackupRecord(ctx, b))
	}
	full, err := s.GetOperationsSummary(ctx, []string{project}, true, now)
	must(err)
	if full.Backups.Recorded != baseline.Backups.Recorded+105 || full.Backups.Latest.ID != prefix+"-backup-104" || full.Backups.LatestVerified.ID != prefix+"-backup-000" {
		t.Fatal("backup summary used truncated records or project scope")
	}
	// The reviewed default policy is checked under the same lock as policy updates.
	policy := core.RetentionPolicy{ProjectID: project, LogDays: 30, RunDays: 90, KeepRuns: 20}
	_, err = s.ApplyRetentionReviewed(ctx, policy, false, now)
	must(err)
	changed := policy
	changed.LogDays = 1
	must(s.SaveRetentionPolicy(ctx, changed))
	old := now.Add(-10 * 24 * time.Hour)
	runID := prefix + "-retention-run"
	must(s.CreateDeployment(ctx, core.Deployment{ID: runID, AppID: fmt.Sprintf("%s-app-%03d", prefix, 1), State: core.DeploymentFailed, CreatedAt: old, FinishedAt: &old}))
	must(s.AppendDeploymentLog(ctx, core.DeploymentLog{DeploymentID: runID, Level: "info", Message: "retained until reviewed", CreatedAt: old}))
	for _, apply := range []bool{false, true} {
		if _, err = s.ApplyRetentionReviewed(ctx, policy, apply, now); !errors.Is(err, ErrRetentionPolicyChanged) {
			t.Fatal("changed retention policy was applied without review", err)
		}
	}
	logs, err := s.ListDeploymentLogs(ctx, runID, 0)
	must(err)
	if len(logs) != 1 {
		t.Fatal("rejected policy review deleted logs")
	}
	_, err = s.ApplyRetentionReviewed(ctx, changed, false, now)
	must(err)
	applied, err := s.ApplyRetentionReviewed(ctx, changed, true, now)
	must(err)
	if applied.Logs != 1 {
		t.Fatal("accepted review did not apply captured policy")
	}
	logs, err = s.ListDeploymentLogs(ctx, runID, 0)
	must(err)
	if len(logs) != 0 {
		t.Fatal("accepted reviewed retention left eligible logs")
	}
	_, err = s.GetDeployment(ctx, runID)
	must(err)
}
