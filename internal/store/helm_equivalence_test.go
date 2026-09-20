package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/doout/dispatch/internal/core"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type equivalenceFixture struct {
	store      *SQLStore
	app        core.App
	server     core.Server
	deployment core.Deployment
	proof      core.HelmEquivalence
}

func newEquivalenceFixture(t *testing.T, dsn string) equivalenceFixture {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for i := 0; i < 2; i++ {
		if err = s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	prefix := fmt.Sprintf("equivalent-%d", now.UnixNano())
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.CreateProject(ctx, core.Project{ID: prefix, Name: prefix, CreatedAt: now}))
	server := core.Server{ID: prefix + "-target", Name: prefix + "-target", Address: "https://cluster.example", Runtime: "kubernetes", State: "ready", Kubernetes: &core.KubernetesServerConfig{Namespace: "default", KubeconfigData: "private-target-material"}, CreatedAt: now}
	must(s.CreateServer(ctx, server))
	server, err = s.GetServer(ctx, server.ID)
	must(err)
	app := core.App{ID: prefix + "-app", Name: prefix + "-app", ProjectID: prefix, ServerID: server.ID, BuildType: core.BuildTypeHelm, Generated: true, State: "ready", HelmChart: "chart", HelmValues: "replicas: 1", CreatedAt: now}
	must(s.CreateApp(ctx, app))
	d := core.Deployment{ID: prefix + "-release", AppID: app.ID, State: core.DeploymentSucceeded, CommitSHA: "original-source", SpecDigest: app.SpecDigest(), Snapshot: core.DeploymentSnapshot{TargetID: server.ID, Namespace: "default", Release: app.Name, Chart: "chart", Values: map[string]any{"replicas": float64(1)}}, CreatedAt: now, FinishedAt: &now}
	must(s.CreateDeployment(ctx, d))
	proof := core.HelmEquivalence{AppID: app.ID, ProjectID: app.ProjectID, AppName: app.Name, AppSpecDigest: app.SpecDigest(), CandidateSpecDigest: app.SpecDigest(), ChartCommit: "new-equivalent-source", ServerID: server.ID, TargetDigest: core.HelmTargetDigest(server), DeploymentID: d.ID, CheckedAt: now.Add(time.Second)}
	return equivalenceFixture{s, app, server, d, proof}
}
func TestHelmEquivalenceSQLite(t *testing.T) {
	testHelmEquivalence(t, filepath.Join(t.TempDir(), "equivalence.db"))
}
func TestHelmEquivalencePostgres(t *testing.T) {
	dsn := os.Getenv("DISPATCH_EQUIVALENCE_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_EQUIVALENCE_POSTGRES_URL to a disposable database")
	}
	testHelmEquivalence(t, dsn)
}
func testHelmEquivalence(t *testing.T, dsn string) {
	f := newEquivalenceFixture(t, dsn)
	s := f.store
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.SaveDriftBaseline(ctx, core.DriftBaseline{DeploymentID: f.deployment.ID, AppID: f.app.ID, ServerID: f.server.ID, Namespace: "default", Release: f.app.Name, Ciphertext: "immutable-baseline"}))
	must(s.AppendDeploymentLog(ctx, core.DeploymentLog{DeploymentID: f.deployment.ID, Level: "info", Message: "real deployment", CreatedAt: f.deployment.CreatedAt}))
	before, err := s.GetDeployment(ctx, f.deployment.ID)
	must(err)
	var analyticsBefore int
	must(s.db.QueryRowContext(ctx, `SELECT count(*) FROM analytics_outbox`).Scan(&analyticsBefore))
	must(s.SaveHelmEquivalence(ctx, f.proof))
	notifications := s.Changes()
	proof, err := s.GetHelmEquivalence(ctx, f.app.ID)
	must(err)
	select {
	case <-notifications:
		t.Fatal("comparison read emitted a write notification")
	default:
	}
	if core.HelmEquivalenceFingerprint(proof) != core.HelmEquivalenceFingerprint(f.proof) {
		t.Fatal("proof inputs did not persist")
	}
	encoded, _ := json.Marshal(proof)
	if string(encoded) != "{}" {
		t.Fatal("private proof serialized", string(encoded))
	}
	after, err := s.GetDeployment(ctx, f.deployment.ID)
	must(err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("comparison changed the retained deployment")
	}
	history, err := s.ListApplicationHistory(ctx, f.app.ID, "", 100)
	must(err)
	if len(history) != 1 {
		t.Fatal("comparison created deployment history")
	}
	baseline, err := s.GetDriftBaseline(ctx, f.deployment.ID)
	must(err)
	if baseline.Ciphertext != "immutable-baseline" {
		t.Fatal("comparison changed drift baseline")
	}
	logs, err := s.ListDeploymentLogs(ctx, f.deployment.ID, 0)
	must(err)
	if len(logs) != 1 {
		t.Fatal("comparison rewrote historical logs")
	}
	var analyticsAfter int
	must(s.db.QueryRowContext(ctx, `SELECT count(*) FROM analytics_outbox`).Scan(&analyticsAfter))
	if analyticsAfter != analyticsBefore {
		t.Fatal("comparison created an analytics deployment event")
	}
	// Candidate and stored values may differ while rendering the same release.
	f.proof.CandidateSpecDigest = "semantically-equivalent-candidate"
	must(s.SaveHelmEquivalence(ctx, f.proof))
	testWorkflowEquivalence(t, f)
}

func TestHelmEquivalenceRejectsStaleProof(t *testing.T) {
	cases := map[string]func(context.Context, *equivalenceFixture) error{
		"application values": func(ctx context.Context, f *equivalenceFixture) error {
			f.app.HelmValues = "replicas: 2"
			return f.store.UpdateApp(ctx, f.app)
		},
		"application name": func(ctx context.Context, f *equivalenceFixture) error {
			f.app.Name += "-renamed"
			return f.store.UpdateApp(ctx, f.app)
		},
		"application project": func(ctx context.Context, f *equivalenceFixture) error {
			id := f.app.ProjectID + "-other"
			if err := f.store.CreateProject(ctx, core.Project{ID: id, Name: id}); err != nil {
				return err
			}
			f.app.ProjectID = id
			return f.store.UpdateApp(ctx, f.app)
		},
		"stored target credentials": func(ctx context.Context, f *equivalenceFixture) error {
			f.server.Kubernetes.KubeconfigData = "rotated-private-target"
			return f.store.UpdateServer(ctx, f.server)
		},
		"default namespace": func(ctx context.Context, f *equivalenceFixture) error {
			f.server.Kubernetes.Namespace = "production"
			return f.store.UpdateServer(ctx, f.server)
		},
		"new successful release": func(ctx context.Context, f *equivalenceFixture) error {
			d := f.deployment
			d.ID += "-new"
			d.CreatedAt = d.CreatedAt.Add(time.Second)
			return f.store.CreateDeployment(ctx, d)
		},
		"newer failed attempt": func(ctx context.Context, f *equivalenceFixture) error {
			d := f.deployment
			d.ID += "-failed"
			d.CreatedAt = d.CreatedAt.Add(time.Second)
			d.State = core.DeploymentFailed
			return f.store.CreateDeployment(ctx, d)
		},
		"older active attempt": func(ctx context.Context, f *equivalenceFixture) error {
			d := f.deployment
			d.ID += "-running"
			d.CreatedAt = d.CreatedAt.Add(-time.Second)
			d.State = core.DeploymentStarting
			return f.store.CreateDeployment(ctx, d)
		},
		"deployment hooks": func(ctx context.Context, f *equivalenceFixture) error {
			f.app.PreDeployHook = "echo do-work"
			return f.store.UpdateApp(ctx, f.app)
		},
		"service binding": func(ctx context.Context, f *equivalenceFixture) error {
			service := core.Service{ID: f.app.ID + "-service", Name: f.app.ID + "-service", ProjectID: f.app.ProjectID, Type: "generic", Revision: 1, Fields: map[string]core.ServiceField{"url": {Value: "db.example", Configured: true}}}
			if err := f.store.CreateService(ctx, service); err != nil {
				return err
			}
			return f.store.ReplaceAppServiceBindings(ctx, f.app.ID, []core.ServiceBinding{{Alias: "db", ServiceRef: service.ID, Helm: &core.ServiceHelmBinding{Keys: map[string]string{"url": "url"}, SecretNameValues: []string{"db.existingSecret"}}}})
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newEquivalenceFixture(t, filepath.Join(t.TempDir(), "state.db"))
			ctx := context.Background()
			if err := f.store.SaveHelmEquivalence(ctx, f.proof); err != nil {
				t.Fatal(err)
			}
			if err := change(ctx, &f); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.GetHelmEquivalence(ctx, f.app.ID); !errors.Is(err, ErrNotFound) {
				t.Fatal("stale proof remained current", err)
			}
			if err := f.store.SaveHelmEquivalence(ctx, f.proof); !errors.Is(err, ErrHelmEquivalenceChanged) {
				t.Fatal("stale proof accepted", err)
			}
		})
	}
}

func TestHelmEquivalenceDoesNotRetainUnusedTarget(t *testing.T) {
	f := newEquivalenceFixture(t, filepath.Join(t.TempDir(), "target.db"))
	ctx := context.Background()
	if err := f.store.SaveHelmEquivalence(ctx, f.proof); err != nil {
		t.Fatal(err)
	}
	target := f.server
	target.ID += "-replacement"
	target.Name = target.ID
	if err := f.store.CreateServer(ctx, target); err != nil {
		t.Fatal(err)
	}
	f.app.ServerID = target.ID
	if err := f.store.UpdateApp(ctx, f.app); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteServer(ctx, f.server.ID); err != nil {
		t.Fatal("stale comparison blocked removal of an unused target", err)
	}
	if _, err := f.store.GetHelmEquivalence(ctx, f.app.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("removed target retained comparison evidence", err)
	}
	if _, err := f.store.GetDeployment(ctx, f.deployment.ID); err != nil {
		t.Fatal("removing comparison evidence deleted real history", err)
	}
}

func testWorkflowEquivalence(t *testing.T, f equivalenceFixture) {
	t.Helper()
	ctx := context.Background()
	s := f.store
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := f.proof.CheckedAt
	secret := core.Secret{ID: f.app.ID + "-source-secret", Name: f.app.ID + "-source-secret", Type: core.SecretTypeGitHubToken, CreatedAt: now, UpdatedAt: now}
	must(s.CreateSecret(ctx, secret))
	source := core.ConfigSource{ID: f.app.ID + "-source", ProjectID: f.app.ProjectID, CredentialSecretID: secret.ID, Name: f.app.ID + "-source", Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}
	must(s.CreateConfigSource(ctx, source))
	resource := core.WorkflowResource{ID: f.app.ID + "-resource", ConfigSourceID: source.ID, Name: f.app.ID + "-resource", Kind: "Application", Active: true, State: "ready", SpecDigest: "resource-spec", CreatedAt: now, UpdatedAt: now}
	must(s.CreateWorkflowResource(ctx, resource))
	revision := core.WorkflowRevision{ID: f.app.ID + "-revision", ResourceID: resource.ID, State: "succeeded", SpecDigest: resource.SpecDigest, CreatedAt: now, FinishedAt: &now}
	must(s.CreateWorkflowRevision(ctx, revision))
	result := core.WorkflowDeploymentResult{DeploymentName: "web", AppID: f.app.ID, DeploymentID: f.deployment.ID, Outcome: "unchanged", Reason: "Release inputs match", CheckedAt: now}
	observed := core.WorkflowEquivalence{ResourceID: resource.ID, ProjectID: f.app.ProjectID, SpecDigest: resource.SpecDigest, BaselineRevisionID: revision.ID, Sources: map[string]core.WorkflowSourceRevision{"chart": {Alias: "chart", CommitSHA: f.proof.ChartCommit}}, Results: []core.WorkflowDeploymentResult{result}, AppProofs: map[string]string{f.app.ID: core.HelmEquivalenceFingerprint(f.proof)}, CheckedAt: now}
	must(s.SaveWorkflowEquivalence(ctx, observed))
	foreign := observed
	foreign.ProjectID += "-other"
	if err := s.SaveWorkflowEquivalence(ctx, foreign); !errors.Is(err, ErrHelmEquivalenceChanged) {
		t.Fatal("cross-project observation accepted", err)
	}
	loaded, err := s.GetWorkflowEquivalence(ctx, resource.ID)
	must(err)
	if !reflect.DeepEqual(loaded.Sources, observed.Sources) || !reflect.DeepEqual(loaded.Results, observed.Results) {
		t.Fatal("resource comparison did not persist")
	}
	encoded, _ := json.Marshal(loaded)
	if strings.Contains(string(encoded), "resource-spec") || strings.Contains(string(encoded), "appProofs") || strings.Contains(string(encoded), f.proof.TargetDigest) {
		t.Fatal("private anchors exposed")
	}
	stage := core.WorkflowStageRun{ID: f.app.ID + "-stage", RevisionID: revision.ID, StageName: "dev", State: "succeeded", DeploymentIDs: []string{f.deployment.ID}, DeploymentResults: []core.WorkflowDeploymentResult{result}, CreatedAt: now}
	must(s.CreateWorkflowStageRun(ctx, stage))
	stage.DeploymentResults[0].Reason = "Retained release reused"
	must(s.UpdateWorkflowStageRun(ctx, stage))
	stored, err := s.GetWorkflowStageRun(ctx, stage.ID)
	must(err)
	if !reflect.DeepEqual(stored.DeploymentResults, stage.DeploymentResults) {
		t.Fatal("stage decision did not persist")
	}
	// Even an older pending execution invalidates a newer successful anchor.
	pending := revision
	pending.ID += "-pending"
	pending.State = "awaiting_approval"
	pending.CreatedAt = now.Add(-time.Second)
	must(s.CreateWorkflowRevision(ctx, pending))
	if _, err = s.GetWorkflowEquivalence(ctx, resource.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("pending work did not invalidate resource observation", err)
	}
	if err = s.SaveWorkflowEquivalence(ctx, observed); !errors.Is(err, ErrHelmEquivalenceChanged) {
		t.Fatal("pending work accepted a no-op", err)
	}
	pending.State = "failed"
	must(s.UpdateWorkflowRevision(ctx, pending))
	must(s.SaveWorkflowEquivalence(ctx, observed))
	resource.State = "invalid"
	must(s.UpdateWorkflowResource(ctx, resource))
	if _, err = s.GetWorkflowEquivalence(ctx, resource.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("invalid configuration reused observation", err)
	}
	resource.State = "ready"
	must(s.UpdateWorkflowResource(ctx, resource))
	newer := revision
	newer.ID += "-newer"
	newer.CreatedAt = now.Add(time.Second)
	must(s.CreateWorkflowRevision(ctx, newer))
	if _, err = s.GetWorkflowEquivalence(ctx, resource.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("new workflow execution retained old observation", err)
	}
	observed.BaselineRevisionID = newer.ID
	must(s.SaveWorkflowEquivalence(ctx, observed))
	// Changing the evaluated proof invalidates the resource cache even when its
	// other fields have not changed. Each cache read binds the exact evaluation.
	f.proof.CheckedAt = now.Add(2 * time.Second)
	must(s.SaveHelmEquivalence(ctx, f.proof))
	if _, err = s.GetWorkflowEquivalence(ctx, resource.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("replaced application proof reused cached observation", err)
	}
	observed.AppProofs[f.app.ID] = core.HelmEquivalenceFingerprint(f.proof)
	must(s.SaveWorkflowEquivalence(ctx, observed))
	resource.SpecDigest = "changed-resource-spec"
	must(s.UpdateWorkflowResource(ctx, resource))
	if _, err = s.GetWorkflowEquivalence(ctx, resource.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("changed definition reused observation", err)
	}
	resource.SpecDigest = observed.SpecDigest
	must(s.UpdateWorkflowResource(ctx, resource))
	// Path-backed credentials can change outside the database. Their successful
	// comparison is retained, but cannot authorize the same-source cache.
	f.server.Kubernetes.KubeconfigData = ""
	f.server.Kubernetes.KubeconfigPath = "/etc/dispatch/mutable-kubeconfig"
	must(s.UpdateServer(ctx, f.server))
	f.server, err = s.GetServer(ctx, f.server.ID)
	must(err)
	f.proof.TargetDigest = core.HelmTargetDigest(f.server)
	must(s.SaveHelmEquivalence(ctx, f.proof))
	observed.AppProofs[f.app.ID] = core.HelmEquivalenceFingerprint(f.proof)
	must(s.SaveWorkflowEquivalence(ctx, observed))
	if _, err = s.GetWorkflowEquivalence(ctx, resource.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("mutable file target authorized a cached skip", err)
	}
}
