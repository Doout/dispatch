package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestTemporaryEnvironmentAdmissionSQLite(t *testing.T) {
	testTemporaryEnvironmentAdmission(t, filepath.Join(t.TempDir(), "temporary.db"))
}
func TestTemporaryEnvironmentAdmissionPostgres(t *testing.T) {
	dsn := os.Getenv("DISPATCH_PROVIDER_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_PROVIDER_POSTGRES_URL for disposable PostgreSQL")
	}
	ctx := context.Background()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "temporary_" + strings.ToLower(ulid.Make().String())
	if _, err = admin.db.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	defer admin.db.ExecContext(ctx, `DROP SCHEMA "`+schema+`" CASCADE`)
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	testTemporaryEnvironmentAdmission(t, u.String())
}
func testTemporaryEnvironmentAdmission(t *testing.T, dsn string) {
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(s.CreateProject(ctx, core.Project{ID: "project", Name: "project", CreatedAt: now}))
	check(s.CreatePrivateNetwork(ctx, core.PrivateNetwork{ID: "node", Name: "node", Driver: "dispatch_agent", Config: map[string]string{}, Details: map[string]string{}, State: "ready", CreatedAt: now, UpdatedAt: now}))
	check(s.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: "node", EnrollmentHash: "hash", EnrollmentExpiresAt: now.Add(time.Hour), UpdatedAt: now}))
	check(s.EnrollEdgeCredential(ctx, "node", "hash", "public", "session", now, now.Add(time.Hour)))
	check(s.CreateServer(ctx, core.Server{ID: "server", Name: "server", Runtime: core.ServerRuntimeDocker, State: "ready", AgentNodeID: "node", AgentMode: "outbound-runtime", CreatedAt: now}))
	template := core.App{ID: "template", ProjectID: "project", ServerID: "server", Name: "template", SourceRepo: "https://github.com/example/environment", Branch: "main", BuildType: core.BuildTypeDockerfile, DockerfilePath: "Dockerfile", Template: true, CreatedAt: now}
	check(s.CreateApp(ctx, template))
	check(s.SaveInfrastructureQuotaPolicy(ctx, core.InfrastructureQuotaPolicy{ProjectID: "project", Revision: 1, MaxTemporaryEnvironments: 1, MaxTemporaryLifetimeSeconds: 7200, UpdatedAt: now}, 0))
	reviews := []core.TemporaryEnvironmentReview{}
	for i := 0; i < 6; i++ {
		id := fmt.Sprint(i)
		clone := template
		clone.ID = "app-" + id
		clone.Name = "environment-" + id
		clone.Template = false
		clone.Generated = true
		clone.Branch = strings.Repeat("a", 40)
		review := core.TemporaryEnvironmentReview{TargetNodeID: "node", TargetGeneration: 1, ID: "review-" + id, EnvironmentID: "environment-" + id, Input: core.TemporaryEnvironmentInput{ProjectID: "project", TemplateID: template.ID, ServerID: "server", Name: clone.Name, SourceSHA: clone.Branch, LifetimeSeconds: 3600}, TemplateDigest: template.SpecDigest(), Clone: clone, Digest: "digest-" + id, State: "prepared", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
		check(s.CreateTemporaryEnvironmentReview(ctx, review))
		reviews = append(reviews, review)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := []core.TemporaryEnvironment{}
	for i, r := range reviews {
		wg.Add(1)
		go func(i int, r core.TemporaryEnvironmentReview) {
			defer wg.Done()
			e, err := s.AcceptTemporaryEnvironment(ctx, r, core.Identity{ID: "owner"}, fmt.Sprint("deployment-", i), now)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				accepted = append(accepted, e)
			} else {
				var quota *core.InfrastructureQuotaViolation
				if !errors.As(err, &quota) {
					t.Errorf("accept: %v", err)
				}
			}
		}(i, r)
	}
	wg.Wait()
	if len(accepted) != 1 {
		t.Fatalf("quota admitted %d environments", len(accepted))
	}
	e := accepted[0]
	apps, err := s.ListAppsForUsage(ctx)
	check(err)
	if len(apps) != 2 {
		t.Fatalf("rejected acceptance leaked app rows: %d", len(apps))
	}
	check(s.ExtendTemporaryEnvironment(ctx, e.ID, e.Revision, e.ExpiresAt.Add(time.Minute), now))
	e, err = s.GetTemporaryEnvironment(ctx, e.ID)
	check(err)
	if err = s.ExtendTemporaryEnvironment(ctx, e.ID, e.Revision, now.Add(3*time.Hour), now); !errors.Is(err, ErrTemporaryEnvironmentChanged) {
		t.Fatal("unbounded extension", err)
	}
	d, err := s.GetDeployment(ctx, e.DeploymentID)
	check(err)
	other := d
	other.ID = "unauthorized-direct-deployment"
	if err = s.CreateDeployment(ctx, other); !errors.Is(err, ErrTemporaryEnvironmentChanged) {
		t.Fatal("direct deployment bypass", err)
	}
	recovered, err := s.RecoverInterruptedDeployments(ctx, now.Add(time.Minute), now.Add(time.Minute))
	check(err)
	if len(recovered) != 0 {
		t.Fatal("accepted environment was abandoned by generic recovery")
	}
	job := core.RuntimeJob{ID: "deploy-" + d.ID, ServerID: e.ServerID, NodeID: "node", NodeGeneration: 1, ProjectID: e.ProjectID, AppID: e.AppID, DeploymentID: d.ID, Operation: "deploy", RequestDigest: "digest", EncryptedRequest: "cipher", ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now}
	check(s.CreateRuntimeJob(ctx, job))
	saved, err := s.GetRuntimeJob(ctx, job.ID)
	check(err)
	if !saved.ExpiresAt.Equal(e.ExpiresAt) {
		t.Fatal("request outlived environment", saved.ExpiresAt, e.ExpiresAt)
	}
	leased, err := s.LeaseRuntimeJob(ctx, "node", now, time.Minute)
	check(err)
	if leased == nil {
		t.Fatal("not leased")
	}
	_, err = s.RenewRuntimeJob(ctx, "node", job.ID, leased.LeaseToken, "building", "", now.Add(time.Second), 2*time.Hour)
	check(err)
	saved, err = s.GetRuntimeJob(ctx, job.ID)
	check(err)
	if saved.LeaseUntil.After(e.ExpiresAt) {
		t.Fatal("lease outlived environment")
	}
	stale, err := s.ClaimTemporaryEnvironment(ctx, e.ID, now)
	check(err)
	check(s.StopTemporaryEnvironment(ctx, e.ID, e.Revision, now.Add(2*time.Second)))
	stale.State = "ready"
	if err = s.SaveTemporaryEnvironment(ctx, stale, now.Add(3*time.Second)); !errors.Is(err, ErrTemporaryEnvironmentChanged) {
		t.Fatal("late reconciler revived stop", err)
	}
	if _, err = s.RenewRuntimeJob(ctx, "node", job.ID, leased.LeaseToken, "ready", "", now.Add(3*time.Second), time.Minute); !errors.Is(err, ErrRuntimeJobConflict) {
		t.Fatal("stale lease renewed", err)
	}
	if err = s.CompleteRuntimeJob(ctx, "node", job.ID, leased.LeaseToken, "succeeded", "result", now.Add(3*time.Second)); !errors.Is(err, ErrRuntimeJobConflict) {
		t.Fatal("stale completion accepted", err)
	}
	saved, err = s.GetRuntimeJob(ctx, job.ID)
	check(err)
	if saved.State != "unknown" || saved.EncryptedRequest != "" {
		t.Fatal("uncertainty was discarded", saved)
	}
	d.State = core.DeploymentSucceeded
	if err = s.UpdateDeployment(ctx, d); err == nil {
		t.Fatal("late success revived stopped environment")
	}
	if err = s.DeleteApp(ctx, e.AppID); !errors.Is(err, ErrTemporaryEnvironmentChanged) {
		t.Fatal("environment history deleted", err)
	}
	// Restart keeps the uncertain original operation and its fencing state.
	check(s.Close())
	s, err = Open(ctx, dsn)
	check(err)
	defer s.Close()
	saved, err = s.GetRuntimeJob(ctx, job.ID)
	check(err)
	if saved.State != "unknown" {
		t.Fatal("restart lost uncertain mutation")
	}
	// Arbitrary runtime mutations cannot bypass the environment lifecycle.
	second := job
	second.ID = "runtime-escape"
	second.Operation = "start"
	if err = s.CreateRuntimeJob(ctx, second); !errors.Is(err, ErrTemporaryEnvironmentChanged) {
		t.Fatal("runtime mutation bypass", err)
	}
	e, err = s.GetTemporaryEnvironment(ctx, e.ID)
	check(err)
	if e.State != "closing" {
		t.Fatal(e.State)
	}
	if _, err = s.GetServer(ctx, e.ServerID); err != nil {
		t.Fatal("shared server removed")
	}
	// Uncertain cleanup cannot release quota through a stale or incorrect checkpoint.
	held, err := s.ClaimTemporaryEnvironment(ctx, e.ID, now.Add(4*time.Second))
	check(err)
	held.State = "closed"
	if err = s.SaveTemporaryEnvironment(ctx, held, now.Add(5*time.Second)); !errors.Is(err, ErrTemporaryEnvironmentChanged) {
		t.Fatal("quota released without verified cleanup", err)
	}
	// A request which never reached an agent expires with a known no-effect result.
	check(s.SaveInfrastructureQuotaPolicy(ctx, core.InfrastructureQuotaPolicy{ProjectID: "project", Revision: 2, MaxTemporaryEnvironments: 2, MaxTemporaryLifetimeSeconds: 7200, UpdatedAt: now}, 1))
	short := reviews[0]
	short.ID = "short-review"
	short.EnvironmentID = "short-environment"
	short.Clone.ID = "short-app"
	short.Clone.Name = "short"
	short.Input.Name = "short"
	short.Input.LifetimeSeconds = 1
	short.CreatedAt = time.Now().UTC()
	short.ExpiresAt = short.CreatedAt.Add(time.Minute)
	check(s.CreateTemporaryEnvironmentReview(ctx, short))
	pending, err := s.AcceptTemporaryEnvironment(ctx, short, core.Identity{ID: "owner"}, "short-deployment", short.CreatedAt)
	check(err)
	waiting := job
	waiting.ID = "deploy-" + pending.DeploymentID
	waiting.AppID = pending.AppID
	waiting.DeploymentID = pending.DeploymentID
	waiting.CreatedAt = short.CreatedAt
	waiting.ExpiresAt = short.CreatedAt.Add(time.Hour)
	check(s.CreateRuntimeJob(ctx, waiting))
	next, err := s.LeaseRuntimeJob(ctx, "node", pending.ExpiresAt.Add(time.Second), time.Minute)
	check(err)
	if next != nil {
		t.Fatal("expired request leased", next)
	}
	expired, err := s.GetRuntimeJob(ctx, waiting.ID)
	check(err)
	if expired.State != "cancelled" || expired.Attempt != 0 || expired.EncryptedRequest != "" {
		t.Fatal("never-leased expiry lost its known outcome", expired)
	}

}
