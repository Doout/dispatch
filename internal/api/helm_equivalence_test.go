package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestHelmEquivalenceSyncPreservesRealReleaseAndObservation(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	projects, err := a.store.ListProjects(ctx)
	must(err)
	now := time.Now().UTC()
	server := core.Server{ID: "equivalent-server", Name: "equivalent-server", Runtime: "kubernetes", State: "ready", Kubernetes: &core.KubernetesServerConfig{Namespace: "default", KubeconfigData: "private-target-input"}, CreatedAt: now}
	must(a.store.CreateServer(ctx, server))
	server, err = a.store.GetServer(ctx, server.ID)
	must(err)
	app := core.App{ID: "equivalent-app", Name: "equivalent-app", ProjectID: projects[0].ID, ServerID: server.ID, BuildType: core.BuildTypeHelm, HelmChart: "chart", HelmValues: "replicas: 1", Generated: true, State: "ready", CreatedAt: now}
	must(a.store.CreateApp(ctx, app))
	d := core.Deployment{ID: "equivalent-deployment", AppID: app.ID, State: core.DeploymentSucceeded, SpecDigest: app.SpecDigest(), CommitSHA: "original-source", Snapshot: core.DeploymentSnapshot{TargetID: server.ID, Release: app.Name, Namespace: "default", Values: map[string]any{"replicas": float64(1)}}, CreatedAt: now, FinishedAt: &now}
	must(a.store.CreateDeployment(ctx, d))
	must(a.store.SaveDriftBaseline(ctx, core.DriftBaseline{DeploymentID: d.ID, AppID: app.ID, ServerID: server.ID, Ciphertext: "retained-encrypted-baseline"}))
	check := core.DriftCheck{DeploymentID: d.ID, State: "synced", Health: "healthy", CheckedAt: &now, Message: "Last observed live resources."}
	must(a.store.SaveDriftCheck(ctx, app.ID, check))
	// Formatting and an unused value changed; only an evaluated render can prove
	// that these desired inputs are equivalent to the retained release.
	app.HelmValues = "replicas: 1\nunused: true\n"
	must(a.store.UpdateApp(ctx, app))
	status, err := a.applicationSync(ctx, app.ID)
	must(err)
	if status.Revision.State != "redeployment_required" {
		t.Fatal("unevaluated change was considered current", status.Revision)
	}
	proof := core.HelmEquivalence{AppID: app.ID, ProjectID: app.ProjectID, AppName: app.Name, AppSpecDigest: app.SpecDigest(), CandidateSpecDigest: app.SpecDigest(), ChartCommit: "evaluated-source", ServerID: server.ID, TargetDigest: core.HelmTargetDigest(server), DeploymentID: d.ID, CheckedAt: now.Add(time.Second)}
	proofs := a.store.(interface {
		SaveHelmEquivalence(context.Context, core.HelmEquivalence) error
	})
	must(proofs.SaveHelmEquivalence(ctx, proof))
	raw := serviceRequestTest(t, a, "GET", "/api/v1/apps/"+app.ID+"/sync", nil, 200)
	must(json.Unmarshal(raw, &status))
	if status.Revision.State != "current" || status.DeploymentID != d.ID || status.Revision.Applied != d.CommitSHA || status.Revision.EvaluatedCommit != proof.ChartCommit || status.Revision.EvaluatedAt == nil || !status.Revision.EvaluatedAt.Equal(proof.CheckedAt) {
		t.Fatal("equivalence replaced real release identity", status)
	}
	if status.Drift.CheckedAt == nil || !status.Drift.CheckedAt.Equal(now) || status.Drift.Health != "healthy" || status.Drift.Message != check.Message {
		t.Fatal("equivalence changed live observation", status.Drift)
	}
	for _, private := range []string{proof.TargetDigest, proof.CandidateSpecDigest, "private-target-input", "retained-encrypted-baseline"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private comparison input escaped API")
		}
	}
	retained, err := a.store.GetDeployment(ctx, d.ID)
	must(err)
	if retained.SpecDigest != d.SpecDigest || retained.CommitSHA != d.CommitSHA || retained.Snapshot.Values["unused"] != nil {
		t.Fatal("retained deployment was rewritten", retained)
	}
	history, err := a.store.ListApplicationHistory(ctx, app.ID, "", 100)
	must(err)
	if len(history) != 1 {
		t.Fatal("equivalence manufactured a deployment", len(history))
	}

	// A workflow may report unchanged while retaining the deployment that was
	// actually applied; a subsequent unevaluated revision must remain visible.
	secret := core.Secret{ID: "equivalent-source-secret", Name: "equivalent-source-secret", Type: core.SecretTypeGitHubToken, CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateSecret(ctx, secret))
	source := core.ConfigSource{ID: "equivalent-config-source", Name: "equivalent-config-source", ProjectID: app.ProjectID, CredentialSecretID: secret.ID, Active: true, State: "synced", CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateConfigSource(ctx, source))
	resource := core.WorkflowResource{ID: "equivalent-resource", Name: "equivalent-resource", ConfigSourceID: source.ID, Kind: "Application", Active: true, State: "ready", SpecDigest: "resource-spec", CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateWorkflowResource(ctx, resource))
	revision := core.WorkflowRevision{ID: "equivalent-original-workflow", ResourceID: resource.ID, State: "succeeded", SpecDigest: resource.SpecDigest, CreatedAt: now, FinishedAt: &now}
	must(a.store.CreateWorkflowRevision(ctx, revision))
	stage := core.WorkflowStageRun{ID: "equivalent-original-stage", RevisionID: revision.ID, StageName: "dev", State: "succeeded", DeploymentIDs: []string{d.ID}, CreatedAt: now}
	must(a.store.CreateWorkflowStageRun(ctx, stage))
	newer := revision
	newer.ID = "equivalent-reused-workflow"
	newer.CreatedAt = now.Add(time.Second)
	newer.Sources = map[string]core.WorkflowSourceRevision{"chart": {Alias: "chart", CommitSHA: proof.ChartCommit}}
	must(a.store.CreateWorkflowRevision(ctx, newer))
	reused := stage
	reused.ID = "equivalent-reused-stage"
	reused.RevisionID = newer.ID
	reused.CreatedAt = newer.CreatedAt
	reused.DeploymentResults = []core.WorkflowDeploymentResult{{DeploymentName: "web", AppID: app.ID, DeploymentID: d.ID, Outcome: "unchanged", CheckedAt: proof.CheckedAt}}
	must(a.store.CreateWorkflowStageRun(ctx, reused))
	status, err = a.applicationSync(ctx, app.ID)
	must(err)
	if status.Revision.State != "current" || status.Revision.Applied != revision.ID || status.Revision.Observed != newer.ID || status.Configuration.LastEvaluatedAt == nil {
		t.Fatal("evaluated workflow did not retain real applied revision", status)
	}
	unevaluated := newer
	unevaluated.ID = "equivalent-new-unchecked-workflow"
	unevaluated.CreatedAt = now.Add(2 * time.Second)
	unevaluated.State = "queued"
	unevaluated.Sources = map[string]core.WorkflowSourceRevision{"chart": {Alias: "chart", CommitSHA: "not-compared"}}
	must(a.store.CreateWorkflowRevision(ctx, unevaluated))
	status, err = a.applicationSync(ctx, app.ID)
	must(err)
	if status.Revision.State != "newer_revision_available" || status.Revision.Observed != unevaluated.ID {
		t.Fatal("new unevaluated revision was suppressed", status.Revision)
	}
	app.HelmValues = "replicas: 2"
	must(a.store.UpdateApp(ctx, app))
	status, err = a.applicationSync(ctx, app.ID)
	must(err)
	if status.Revision.EvaluatedAt != nil || status.Revision.EvaluatedCommit != "" || status.Configuration.LastEvaluatedAt != nil {
		t.Fatal("stale proof survived application edit", status)
	}
}
