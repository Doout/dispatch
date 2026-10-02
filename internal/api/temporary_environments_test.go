package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/store"
)

func temporaryFixture(t *testing.T) (*API, core.App) {
	t.Helper()
	a, node, session, app := runtimeAPIFixture(t)
	ctx := context.Background()
	runtimeNodeRequest(t, a, "GET", "/api/v1/edge/nodes/"+node.ID+"/runtime/jobs/next", session.Token, nil, 204)
	app.Template = true
	app.BuildType = core.BuildTypeDockerfile
	app.ComposeContent = ""
	app.SourceRepo = "https://github.com/example/environment"
	app.Branch = "main"
	app.DockerfilePath = "Dockerfile"
	app.PreDeployHook = "do-not-copy"
	app.Domain = "production.example.test"
	if err := a.store.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	server, err := a.store.GetServer(ctx, app.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	server.State = "ready"
	if err = a.store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	n, err := a.store.GetPrivateNetwork(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	n.Details["runtimeCapabilities"] = "deploy,inspect,destroy"
	n.Details["runtimeCheckedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	n.Details["runtimeVersion"] = remoteruntime.APIVersion
	if err = a.store.UpdatePrivateNetwork(ctx, n); err != nil {
		t.Fatal(err)
	}
	data := a.store.(*store.SQLStore)
	policy := core.InfrastructureQuotaPolicy{ProjectID: app.ProjectID, Revision: 1, MaxTemporaryEnvironments: 3, MaxTemporaryLifetimeSeconds: 7200, UpdatedAt: time.Now().UTC()}
	if err = data.SaveInfrastructureQuotaPolicy(ctx, policy, 0); err != nil {
		t.Fatal(err)
	}
	return a, app
}
func temporaryReviewTest(t *testing.T, a *API, app core.App, name string) core.TemporaryEnvironmentReview {
	t.Helper()
	raw := serviceRequestTest(t, a, "POST", "/api/v1/temporary-environments/review", core.TemporaryEnvironmentInput{ProjectID: app.ProjectID, TemplateID: app.ID, ServerID: app.ServerID, Name: name, SourceSHA: strings.Repeat("a", 40), LifetimeSeconds: 3600}, 201)
	var r core.TemporaryEnvironmentReview
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func temporaryKeyed(a *API, path, key string, input any) *httptest.ResponseRecorder {
	body, _ := json.Marshal(input)
	r := tokenRequest("POST", path, bytes.NewReader(body))
	r.Header.Set("Idempotency-Key", key)
	out := httptest.NewRecorder()
	a.ServeHTTP(out, r)
	return out
}
func TestTemporaryEnvironmentReviewAtomicReplayAndCleanupReplay(t *testing.T) {
	a, app := temporaryFixture(t)
	r := temporaryReviewTest(t, a, app, "investigation")
	if r.Clone.PreDeployHook != "" || r.Clone.Domain != "" || r.Clone.Template || !r.Clone.Generated || len(r.Omissions) < 4 {
		t.Fatal("unsafe clone", r)
	}
	input := temporaryAcceptance{ReviewID: r.ID, Digest: r.Digest, ConfirmName: r.Input.Name}
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- temporaryKeyed(a, "/api/v1/temporary-environments", "create-environment-1", input)
		}()
	}
	wg.Wait()
	close(responses)
	ids := map[string]bool{}
	for response := range responses {
		if response.Code != 202 {
			t.Fatal(response.Code, response.Body.String())
		}
		var receipt core.MutationReceipt
		json.Unmarshal(response.Body.Bytes(), &receipt)
		ids[receipt.OperationID] = true
	}
	if len(ids) != 1 {
		t.Fatal("duplicate deployment identities", ids)
	}
	data := a.store.(*store.SQLStore)
	items, err := data.ListTemporaryEnvironments(context.Background(), app.ProjectID)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	e := items[0]
	response := temporaryKeyed(a, "/api/v1/temporary-environments", "different-create-key", input)
	if response.Code != 409 {
		t.Fatal(response.Code, response.Body.String())
	}
	var cleanup temporaryCleanupReview
	json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/temporary-environments/"+e.ID+"/cleanup-review", nil, 200), &cleanup)
	closeInput := map[string]any{"revision": cleanup.Revision, "digest": cleanup.Digest, "confirmName": e.Name}
	first := temporaryKeyed(a, "/api/v1/temporary-environments/"+e.ID+"/destroy", "destroy-environment-1", closeInput)
	if first.Code != 202 {
		t.Fatal(first.Code, first.Body.String())
	}
	second := temporaryKeyed(a, "/api/v1/temporary-environments/"+e.ID+"/destroy", "destroy-environment-1", closeInput)
	if second.Code != 202 || second.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal(second.Code, second.Body.String())
	}
	var x, y core.MutationReceipt
	json.Unmarshal(first.Body.Bytes(), &x)
	json.Unmarshal(second.Body.Bytes(), &y)
	if x.OperationID != y.OperationID {
		t.Fatal("cleanup changed on retry")
	}
	audit, err := a.store.(operationsStore).ListAuditEvents(context.Background(), core.AuditFilter{ProjectIDs: []string{e.ProjectID}, AppID: e.AppID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	linked := 0
	for _, event := range audit {
		if event.OperationID == x.OperationID && event.ConfirmedName == e.Name && event.ConfirmedVersion == cleanup.Digest {
			linked++
		}
	}
	if linked != 2 {
		t.Fatal("cleanup replay audit lost scoped review", audit)
	}
	saved, _ := data.GetTemporaryEnvironment(context.Background(), e.ID)
	if saved.State != "closing" || saved.CleanupOperationID != x.OperationID {
		t.Fatal(saved)
	}
	dep, _ := data.GetDeployment(context.Background(), e.DeploymentID)
	if dep.State != core.DeploymentCancelled {
		t.Fatal(dep.State)
	}
	dep.State = core.DeploymentSucceeded
	if err = data.UpdateDeployment(context.Background(), dep); err == nil {
		t.Fatal("late deployment revived closed intent")
	}
	raw := serviceRequestTest(t, a, "GET", "/api/v1/projects/"+app.ProjectID+"/infrastructure/quota", nil, 200)
	if !bytes.Contains(raw, []byte(`"temporaryEnvironments":1`)) {
		t.Fatal(string(raw))
	}
}
func TestTemporaryEnvironmentReviewRejectsUnsupportedTargetsAndPolicy(t *testing.T) {
	a, app := temporaryFixture(t)
	input := core.TemporaryEnvironmentInput{ProjectID: app.ProjectID, TemplateID: app.ID, ServerID: app.ServerID, Name: "test-environment", SourceSHA: strings.Repeat("a", 40), LifetimeSeconds: 9000}
	serviceRequestTest(t, a, "POST", "/api/v1/temporary-environments/review", input, 422)
	input.LifetimeSeconds = 3600
	server, _ := a.store.GetServer(context.Background(), app.ServerID)
	server.State = "pending"
	a.store.UpdateServer(context.Background(), server)
	serviceRequestTest(t, a, "POST", "/api/v1/temporary-environments/review", input, 422)
	raw := serviceRequestTest(t, a, "GET", "/api/v1/projects/"+app.ProjectID+"/temporary-environments/options", nil, 200)
	var options struct {
		Targets []any `json:"targets"`
	}
	json.Unmarshal(raw, &options)
	if len(options.Targets) != 0 {
		t.Fatal("unready target offered")
	}
}

func TestTemporaryEnvironmentRestartReconcilesOwnedRuntimeAndRetainsData(t *testing.T) {
	a, template := temporaryFixture(t)
	ctx := context.Background()
	data := a.store.(*store.SQLStore)
	review := temporaryReviewTest(t, a, template, "restart-rehearsal")
	response := temporaryKeyed(a, "/api/v1/temporary-environments", "restart-create-key", temporaryAcceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Input.Name})
	if response.Code != 202 {
		t.Fatal(response.Body.String())
	}
	e, err := data.GetTemporaryEnvironment(ctx, review.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	app, err := data.GetApp(ctx, e.AppID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := data.GetServer(ctx, e.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := data.GetDeployment(ctx, e.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	// Shutdown/read failure after claiming work must not become a cleanup intent.
	claimed, err := data.ClaimTemporaryEnvironment(ctx, e.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	interrupted, cancel := context.WithCancel(ctx)
	cancel()
	a.reconcileTemporaryEnvironment(interrupted, data, claimed)
	e, err = data.GetTemporaryEnvironment(ctx, e.ID)
	if err != nil || e.State != "accepted" {
		t.Fatal("interrupted read triggered cleanup", e, err)
	}
	// Persist a real typed request/result without a live controller deployment goroutine.
	broker := a.runtimeBroker()
	job, err := broker.Submit(ctx, "deploy-"+d.ID, remoteruntime.NewRequest("deploy", d, app, server))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := broker.Lease(ctx, server.AgentNodeID)
	if err != nil || lease == nil || lease.ID != job.ID {
		t.Fatal(lease, err)
	}
	health := d.Health
	health.State = "passed"
	for _, check := range health.Policy.Checks {
		health.Checks = append(health.Checks, core.HealthCheckResult{Check: check, State: "passed", Attempts: 1})
	}
	if err = broker.Complete(ctx, server.AgentNodeID, job.ID, remoteruntime.Completion{LeaseToken: lease.LeaseToken, Result: remoteruntime.Result{State: "succeeded", Health: &health}}); err != nil {
		t.Fatal(err)
	}
	a.reconcileTemporaryEnvironments(ctx)
	e, err = data.GetTemporaryEnvironment(ctx, e.ID)
	if err != nil || e.State != "ready" {
		t.Fatal(e, err)
	}
	saved, err := data.GetDeployment(ctx, d.ID)
	if err != nil || saved.State != core.DeploymentSucceeded {
		t.Fatal(saved, err)
	}
	volume := core.StorageResource{ID: "retained-volume", ServerID: e.ServerID, Kind: "docker_volume", Name: "retained", Identity: "volume-id", ProjectID: e.ProjectID, OwnerKind: "application", OwnerID: e.AppID, Ownership: "verified", Policy: "retain", State: "present", Revision: 1, ObservedAt: time.Now().UTC()}
	if err = data.ObserveStorage(ctx, volume); err != nil {
		t.Fatal(err)
	}
	var cleanup temporaryCleanupReview
	json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/temporary-environments/"+e.ID+"/cleanup-review", nil, 200), &cleanup)
	found := false
	for _, r := range cleanup.Resources {
		if r.ID == volume.ID && r.Ownership == "retained" {
			found = true
		}
	}
	if !found {
		t.Fatal("retention omitted from review")
	}
	response = temporaryKeyed(a, "/api/v1/temporary-environments/"+e.ID+"/destroy", "restart-destroy-key", map[string]any{"revision": e.Revision, "digest": cleanup.Digest, "confirmName": e.Name})
	if response.Code != 202 {
		t.Fatal(response.Code, response.Body.String())
	}
	finished := make(chan struct{})
	go func() { defer close(finished); a.reconcileTemporaryEnvironments(ctx) }()
	var clean *remoteruntime.LeasedJob
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		clean, err = broker.Lease(ctx, server.AgentNodeID)
		if err != nil {
			t.Fatal(err)
		}
		if clean != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if clean == nil || clean.Request.Operation != "destroy" || clean.Request.Application.ID != e.AppID {
		t.Fatal("cleanup was not the owned durable operation", clean)
	}
	if err = broker.Complete(ctx, server.AgentNodeID, clean.ID, remoteruntime.Completion{LeaseToken: clean.LeaseToken, Result: remoteruntime.Result{State: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not settle")
	}
	e, err = data.GetTemporaryEnvironment(ctx, e.ID)
	if err != nil || e.State != "closed" {
		t.Fatal(e, err)
	}
	if _, err = data.GetServer(ctx, server.ID); err != nil {
		t.Fatal("shared server removed")
	}
	v, err := data.GetStorage(ctx, volume.ID)
	if err != nil || v.Policy != "retain" || v.State != "present" {
		t.Fatal("retained data changed", v, err)
	}
	if _, err = data.GetDeployment(ctx, d.ID); err != nil {
		t.Fatal("history deleted")
	}
	var createdReceipt core.MutationReceipt
	// The original create receipt remains succeeded when its environment is later removed.
	original := temporaryKeyed(a, "/api/v1/temporary-environments", "restart-create-key", temporaryAcceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Input.Name})
	if original.Code != 202 {
		t.Fatal(original.Body.String())
	}
	json.Unmarshal(original.Body.Bytes(), &createdReceipt)
	if createdReceipt.State != "succeeded" {
		t.Fatal("cleanup rewrote create outcome", createdReceipt)
	}
	a.reconcileTemporaryEnvironments(ctx)
	if next, err := broker.Lease(ctx, server.AgentNodeID); err != nil || next != nil {
		t.Fatal("closed environment created more work", next, err)
	}
}
func TestTemporaryEnvironmentRevokedAutomationCredentialFencesWork(t *testing.T) {
	a, template := temporaryFixture(t)
	ctx := context.Background()
	data := a.store.(*store.SQLStore)
	review := temporaryReviewTest(t, a, template, "revocation-test")
	saved, err := data.GetTemporaryEnvironmentReview(ctx, review.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	account := core.ServiceAccount{ID: "environment-agent", Name: "environment-agent", State: "active", CreatedAt: now, UpdatedAt: now}
	if err = data.CreateServiceAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	credential := core.AutomationCredential{ID: "environment-token", AccountID: account.ID, Name: "test", TokenHash: strings.Repeat("a", 64), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err = data.IssueAutomationCredential(ctx, credential, ""); err != nil {
		t.Fatal(err)
	}
	if err = data.SavePrincipalGrant(ctx, core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: template.ProjectID, Permissions: []core.Permission{core.PermissionProjectView, core.PermissionProjectConfigure, core.PermissionDeploymentRun}, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = data.SaveInfrastructureAssignment(ctx, core.InfrastructureAssignment{ProjectID: template.ProjectID, Kind: "target", ResourceID: template.ServerID, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	e, err := data.AcceptTemporaryEnvironment(ctx, saved, core.Identity{Kind: core.PrincipalServiceAccount, ID: account.ID, CredentialID: credential.ID}, "revoked-deployment", now)
	if err != nil {
		t.Fatal(err)
	}
	app, err := data.GetApp(ctx, e.AppID)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.checkTemporaryExecution(ctx, app, e.SourceSHA); err != nil {
		t.Fatal(err)
	}
	if err = data.RevokeAutomationCredential(ctx, account.ID, credential.ID, now); err != nil {
		t.Fatal(err)
	}
	if err = a.checkTemporaryExecution(ctx, app, e.SourceSHA); err == nil {
		t.Fatal("revoked credential continued")
	}
	e, _ = data.GetTemporaryEnvironment(ctx, e.ID)
	if e.State != "closing" {
		t.Fatal(e.State)
	}
}
