package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

func queuedBackupPolicyRuntime(t *testing.T) (*API, core.PrivateNetwork, edge.Session, core.WorkloadBackupPolicy, core.WorkloadBackupRequest) {
	t.Helper()
	return queuedBackupPolicyRuntimeForActor(t, false)
}

func queuedBackupPolicyRuntimeForActor(t *testing.T, scoped bool) (*API, core.PrivateNetwork, edge.Session, core.WorkloadBackupPolicy, core.WorkloadBackupRequest) {
	t.Helper()
	a, _, template := resourceAPIFixture(t)
	ctx := context.Background()
	var node core.PrivateNetwork
	raw := serviceRequestTest(t, a, "POST", "/api/v1/private-networks", map[string]string{"name": "backup-runtime", "driver": "dispatch_agent"}, 201)
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatal(err)
	}
	session := enrollNodeTest(t, a, node)
	server, err := a.store.GetServer(ctx, "resource-target")
	if err != nil {
		t.Fatal(err)
	}
	server.AgentNodeID = node.ID
	if err = a.store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	var run core.ServiceProvisionRun
	raw = serviceRequestTest(t, a, "POST", "/api/v1/service-templates/"+template.ID+"/runs", map[string]any{"name": "queued-backup-db"}, 202)
	if err = json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	source := awaitServiceResource(t, a, run.ID, "ready")
	a.deploy.Storage.Backend = &storageTestBackend{observation: core.StorageObservation{Resource: core.StorageResource{Kind: "docker_volume", Name: deploy.ServiceResourceName(run.ID) + "-data", Identity: "owned-volume", Evidence: "labels"}, Labels: map[string]string{"dispatch.managed-by": "dispatch", "dispatch.project": source.ProjectID, "dispatch.service-template": template.ID, "dispatch.service-provision": run.ID}}}
	input := map[string]any{"name": "runtime-backup", "sourceRunId": source.RunID, "intervalHours": 1, "keepLast": 1, "confirmRetention": "runtime-backup"}
	token := "secret"
	if scoped {
		token, _ = offsiteIdentity(t, a, source.ProjectID)
		automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+source.ProjectID, map[string]any{"kind": "target", "resourceId": source.Target.ServerID}, 200)
	}
	accepted := mutationRequest(a, token, "POST", "/api/v1/workload-backup-policies", "runtime-policy", input)
	if accepted.Code != 201 {
		t.Fatal(accepted.Code, accepted.Body.String())
	}
	p, err := a.store.(store.WorkloadBackupPolicyStore).GetWorkloadBackupPolicy(ctx, decodeMutation(t, accepted).OperationID)
	if err != nil {
		t.Fatal(err)
	}
	request, err := a.decryptWorkloadBackup(p.ID, "policy", p.EncryptedInput)
	if err != nil {
		t.Fatal(err)
	}
	request.OperationID = "queued-policy-capture"
	request.Backup.ID = request.OperationID
	request.Backup.ArtifactID = request.Backup.ID
	request.Backup.CapturePolicyID = p.ID
	request.Backup.State = "creating"
	request.Backup.Revision = 1
	request.Backup.CreatedAt = time.Now().UTC()
	request.Backup.UpdatedAt = request.Backup.CreatedAt
	request.Backup.EncryptedInput, err = a.encryptWorkloadBackup(request.Backup.ID, "accepted", request)
	if err != nil {
		t.Fatal(err)
	}
	op := newBackupOperation(request.OperationID, request.Backup, "backup")
	op.CapturePolicyID = p.ID
	op.EncryptedInput, err = a.encryptWorkloadBackup(op.ID, "operation", request)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.store.(store.WorkloadBackupStore).CreateWorkloadBackup(ctx, request.Backup, op); err != nil {
		t.Fatal(err)
	}
	if _, err = a.runtimeBroker().Submit(ctx, "backup-"+op.ID, remoteruntime.NewWorkloadBackupRequest(request, server)); err != nil {
		t.Fatal(err)
	}
	return a, node, session, p, request
}

func backupRuntimeNodeRequest(t *testing.T, a *API, node core.PrivateNetwork, token, suffix, method string, input any, want int) []byte {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "/api/v1/edge/nodes/"+node.ID+"/runtime/jobs/"+suffix, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Dispatch-Runtime-Version", remoteruntime.APIVersion)
	req.Header.Set("X-Dispatch-Runtime-Capabilities", "workload_backup,workload_backup_inspect,inspect")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatal(method, suffix, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

func pauseRuntimeBackupPolicy(t *testing.T, a *API, p core.WorkloadBackupPolicy) {
	t.Helper()
	p.Enabled = false
	if err := a.store.(store.WorkloadBackupPolicyStore).UpdateWorkloadBackupPolicy(context.Background(), p, p.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestScheduledBackupDeniedFirstDispatchStopsWithoutReclaim(t *testing.T) {
	a, node, session, policy, request := queuedBackupPolicyRuntime(t)
	pauseRuntimeBackupPolicy(t, a, policy)
	backupRuntimeNodeRequest(t, a, node, session.Token, "next", "GET", nil, 204)
	job, err := a.runtimeBroker().Store.GetRuntimeJob(context.Background(), "backup-"+request.OperationID)
	if err != nil || job.State != "failed" || job.Attempt != 1 || job.EncryptedRequest != "" || job.EncryptedResult == "" {
		t.Fatal("undispatched job remained reclaimable", job.State, job.Attempt, err)
	}
	result, err := a.runtimeBroker().Wait(context.Background(), job.ID, nil)
	if err == nil || result.WorkloadBackup == nil || result.WorkloadBackup.State != "failed" || result.WorkloadBackup.CleanupState != "complete" {
		t.Fatal("unsent work lost its confirmed cleanup outcome", err)
	}
	backupRuntimeNodeRequest(t, a, node, session.Token, "next", "GET", nil, 204)
	job, _ = a.runtimeBroker().Store.GetRuntimeJob(context.Background(), job.ID)
	if job.Attempt != 1 {
		t.Fatal("denied job reclaimed the runtime lease")
	}
}

func TestBackupUnsupportedFirstDispatchRecordsCompleteCleanup(t *testing.T) {
	a, node, session, _, request := queuedBackupPolicyRuntime(t)
	req := httptest.NewRequest("GET", "/api/v1/edge/nodes/"+node.ID+"/runtime/jobs/next", nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	req.Header.Set("X-Dispatch-Runtime-Version", remoteruntime.APIVersion)
	req.Header.Set("X-Dispatch-Runtime-Capabilities", "inspect")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatal("unsupported job payload was delivered", w.Code)
	}
	result, err := a.runtimeBroker().Wait(context.Background(), "backup-"+request.OperationID, nil)
	if err == nil || result.State != "failed" || result.Code != runtimecontract.Unsupported || result.WorkloadBackup == nil || result.WorkloadBackup.CleanupState != "complete" {
		t.Fatal("unsupported execution lost its proven cleanup outcome", err)
	}
}

func TestScheduledBackupDeniedReofferPreservesUnknownAndRecoveryEvidence(t *testing.T) {
	a, node, session, policy, request := queuedBackupPolicyRuntime(t)
	ctx := context.Background()
	jobs := a.runtimeBroker().Store
	prior, err := jobs.LeaseRuntimeJob(ctx, node.ID, time.Now().Add(-2*remoteruntime.LeaseDuration), remoteruntime.LeaseDuration)
	if err != nil || prior == nil || prior.Attempt != 1 {
		t.Fatal("original execution lease missing", err)
	}
	pauseRuntimeBackupPolicy(t, a, policy)
	backupRuntimeNodeRequest(t, a, node, session.Token, "next", "GET", nil, 204)
	job, err := jobs.GetRuntimeJob(ctx, prior.ID)
	if err != nil || job.State != "unknown" || job.Attempt != 2 || !job.CancelRequested || job.EncryptedRequest != "" {
		t.Fatal("uncertain operation was not fenced", job.State, job.Attempt, err)
	}
	op, err := a.store.(store.WorkloadBackupStore).GetWorkloadBackupOperation(ctx, request.OperationID)
	if err != nil || op.EncryptedInput == "" {
		t.Fatal("fencing erased recovery evidence", err)
	}
	if err = jobs.FenceRuntimeJob(ctx, node.ID, job.ID, prior.LeaseToken, time.Now()); !errors.Is(err, store.ErrRuntimeJobConflict) {
		t.Fatal("stale lease fenced a current job", err)
	}
	server, _ := a.store.GetServer(ctx, policy.ServerID)
	app := core.App{ID: "after-fenced-backup", ProjectID: policy.ProjectID, ServerID: server.ID, Name: "Following inspection", BuildType: core.BuildTypeCompose, ComposeContent: "services: {}", CreatedAt: time.Now()}
	if err = a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if _, err = a.runtimeBroker().Submit(ctx, "following-inspection", remoteruntime.NewRequest(runtimecontract.Inspect, core.Deployment{}, app, server)); err != nil {
		t.Fatal(err)
	}
	var next remoteruntime.LeasedJob
	raw := backupRuntimeNodeRequest(t, a, node, session.Token, "next", "GET", nil, 200)
	if json.Unmarshal(raw, &next) != nil || next.ID != "following-inspection" {
		t.Fatal("fenced job blocked following runtime work")
	}
}

func TestScheduledBackupRenewalDenialRetainsOriginalCompletionEvidence(t *testing.T) {
	a, node, session, policy, request := queuedBackupPolicyRuntime(t)
	var lease remoteruntime.LeasedJob
	raw := backupRuntimeNodeRequest(t, a, node, session.Token, "next", "GET", nil, 200)
	if json.Unmarshal(raw, &lease) != nil {
		t.Fatal("invalid lease")
	}
	pauseRuntimeBackupPolicy(t, a, policy)
	// Neither a stale lease nor another enrolled node can request cancellation.
	backupRuntimeNodeRequest(t, a, node, session.Token, lease.ID+"/heartbeat", "POST", remoteruntime.Heartbeat{LeaseToken: "stale-token"}, 409)
	var other core.PrivateNetwork
	raw = serviceRequestTest(t, a, "POST", "/api/v1/private-networks", map[string]string{"name": "other-backup-runtime", "driver": "dispatch_agent"}, 201)
	if err := json.Unmarshal(raw, &other); err != nil {
		t.Fatal(err)
	}
	otherSession := enrollNodeTest(t, a, other)
	backupRuntimeNodeRequest(t, a, other, otherSession.Token, lease.ID+"/heartbeat", "POST", remoteruntime.Heartbeat{LeaseToken: lease.LeaseToken}, 404)
	before, err := a.runtimeBroker().Store.GetRuntimeJob(context.Background(), lease.ID)
	if err != nil || before.CancelRequested || before.LeaseToken != lease.LeaseToken || before.State != "running" {
		t.Fatal("an unauthorized heartbeat changed another lease", err)
	}
	backupRuntimeNodeRequest(t, a, node, session.Token, lease.ID+"/heartbeat", "POST", remoteruntime.Heartbeat{LeaseToken: lease.LeaseToken}, 422)
	job, err := a.runtimeBroker().Store.GetRuntimeJob(context.Background(), lease.ID)
	if err != nil || job.State != "running" || !job.CancelRequested {
		t.Fatal("renewal denial erased the active execution lease", job.State, err)
	}
	result := core.WorkloadBackupResult{BackupID: request.Backup.ID, ProjectID: request.Backup.ProjectID, OperationID: request.OperationID, ArtifactID: request.Backup.ID, State: "ready", Checksum: strings.Repeat("a", 64), Bytes: 42, CleanupState: "complete"}
	backupRuntimeNodeRequest(t, a, node, session.Token, lease.ID+"/complete", "POST", remoteruntime.Completion{LeaseToken: lease.LeaseToken, Result: remoteruntime.Result{State: "succeeded", WorkloadBackup: &result}}, 204)
	job, err = a.runtimeBroker().Store.GetRuntimeJob(context.Background(), lease.ID)
	if err != nil || job.State != "succeeded" || job.EncryptedResult == "" {
		t.Fatal("valid original execution evidence was discarded", job.State, err)
	}
}

func queuedBackupPolicyReconciliation(t *testing.T) (*API, core.PrivateNetwork, edge.Session, core.WorkloadBackupPolicy, core.WorkloadBackupRequest, string) {
	t.Helper()
	a, node, session, p, request := queuedBackupPolicyRuntimeForActor(t, true)
	ctx := context.Background()
	if err := a.runtimeBroker().Store.ExpireRuntimeJobs(ctx, time.Now().UTC().Add(31*time.Minute)); err != nil {
		t.Fatal(err)
	}
	request.Action, request.RecoveryAction = "reconcile", "backup"
	server, err := a.store.GetServer(ctx, p.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	jobID := ulid.Make().String()
	if _, err = a.runtimeBroker().Submit(ctx, jobID, remoteruntime.NewWorkloadBackupRequest(request, server)); err != nil {
		t.Fatal(err)
	}
	return a, node, session, p, request, jobID
}

func TestScheduledBackupRandomReconciliationRevocationBeforeLease(t *testing.T) {
	a, node, session, p, _, jobID := queuedBackupPolicyReconciliation(t)
	if err := a.store.(store.AutomationStore).RevokeAutomationCredential(context.Background(), p.Actor.ID, p.Actor.CredentialID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	raw := backupRuntimeNodeRequest(t, a, node, session.Token, "next", "GET", nil, 204)
	if len(raw) != 0 {
		t.Fatal("revoked actor received backup decryption input")
	}
	job, err := a.runtimeBroker().Store.GetRuntimeJob(context.Background(), jobID)
	if err != nil || job.State != "failed" || job.EncryptedRequest != "" {
		t.Fatal("random reconciliation remained dispatchable", job.State, err)
	}
}

func TestScheduledBackupRandomReconciliationRevocationBeforeRenewal(t *testing.T) {
	a, node, session, p, request, jobID := queuedBackupPolicyReconciliation(t)
	var lease remoteruntime.LeasedJob
	if err := json.Unmarshal(backupRuntimeNodeRequest(t, a, node, session.Token, "next", "GET", nil, 200), &lease); err != nil || lease.ID != jobID {
		t.Fatal("random recovery did not lease", err)
	}
	if err := a.store.(store.AutomationStore).RevokeAutomationCredential(context.Background(), p.Actor.ID, p.Actor.CredentialID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	backupRuntimeNodeRequest(t, a, node, session.Token, jobID+"/heartbeat", "POST", remoteruntime.Heartbeat{LeaseToken: "stale-lease"}, 409)
	job, _ := a.runtimeBroker().Store.GetRuntimeJob(context.Background(), jobID)
	if job.CancelRequested {
		t.Fatal("stale heartbeat cancelled legitimate inspection")
	}
	backupRuntimeNodeRequest(t, a, node, session.Token, jobID+"/heartbeat", "POST", remoteruntime.Heartbeat{LeaseToken: lease.LeaseToken}, 422)
	job, _ = a.runtimeBroker().Store.GetRuntimeJob(context.Background(), jobID)
	if !job.CancelRequested || job.State != "running" {
		t.Fatal("revoked policy renewed random inspection", job.State)
	}
	result := core.WorkloadBackupResult{BackupID: request.Backup.ID, ProjectID: request.Backup.ProjectID, OperationID: request.OperationID, ArtifactID: request.Backup.ID, State: "ready", Checksum: strings.Repeat("a", 64), Bytes: 42, CleanupState: "complete"}
	backupRuntimeNodeRequest(t, a, node, session.Token, jobID+"/complete", "POST", remoteruntime.Completion{LeaseToken: lease.LeaseToken, Result: remoteruntime.Result{State: "succeeded", WorkloadBackup: &result}}, 204)
	job, _ = a.runtimeBroker().Store.GetRuntimeJob(context.Background(), jobID)
	if job.State != "succeeded" || job.EncryptedResult == "" {
		t.Fatal("revocation lost original legitimate inspection result")
	}
}

func TestScheduledBackupRandomReconciliationRejectsChangedSourceIdentity(t *testing.T) {
	a, node, session, p, request := queuedBackupPolicyRuntimeForActor(t, true)
	ctx := context.Background()
	if err := a.runtimeBroker().Store.ExpireRuntimeJobs(ctx, time.Now().UTC().Add(31*time.Minute)); err != nil {
		t.Fatal(err)
	}
	request.Action, request.RecoveryAction = "reconcile", "backup"
	request.Backup.SourceResourceID = "another-owned-container"
	server, _ := a.store.GetServer(ctx, p.ServerID)
	jobID := ulid.Make().String()
	if _, err := a.runtimeBroker().Submit(ctx, jobID, remoteruntime.NewWorkloadBackupRequest(request, server)); err != nil {
		t.Fatal(err)
	}
	backupRuntimeNodeRequest(t, a, node, session.Token, "next", "GET", nil, 204)
	job, _ := a.runtimeBroker().Store.GetRuntimeJob(ctx, jobID)
	if job.State != "failed" || job.EncryptedRequest != "" {
		t.Fatal("changed source identity released original encryption keys")
	}
}

func TestManualBackupRecoveryDoesNotInheritRevokedPolicyActor(t *testing.T) {
	a, node, session, p, request := queuedBackupPolicyRuntimeForActor(t, true)
	ctx := context.Background()
	backups := a.store.(store.WorkloadBackupStore)
	if err := a.runtimeBroker().Store.ExpireRuntimeJobs(ctx, time.Now().UTC().Add(31*time.Minute)); err != nil {
		t.Fatal(err)
	}
	op, err := backups.GetWorkloadBackupOperation(ctx, request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := backups.GetWorkloadBackup(ctx, request.Backup.ID)
	if err != nil {
		t.Fatal(err)
	}
	b.State, b.Checksum = "ready", strings.Repeat("a", 64)
	op.State, op.CleanupState = "succeeded", "complete"
	if err = backups.CompleteWorkloadBackupOperation(ctx, b, op); err != nil {
		t.Fatal(err)
	}
	b, err = backups.GetWorkloadBackup(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	manual := newBackupOperation("owner-reviewed-verification", b, "verify")
	request.OperationID, request.Action, request.Backup = manual.ID, "verify", b
	manual.EncryptedInput, err = a.encryptWorkloadBackup(manual.ID, "operation", request)
	if err != nil {
		t.Fatal(err)
	}
	if err = backups.CreateWorkloadBackupOperation(ctx, manual, b.Revision); err != nil {
		t.Fatal(err)
	}
	request.Action, request.RecoveryAction = "reconcile", "verify"
	server, _ := a.store.GetServer(ctx, p.ServerID)
	jobID := ulid.Make().String()
	if _, err = a.runtimeBroker().Submit(ctx, jobID, remoteruntime.NewWorkloadBackupRequest(request, server)); err != nil {
		t.Fatal(err)
	}
	if err = a.store.(store.AutomationStore).RevokeAutomationCredential(ctx, p.Actor.ID, p.Actor.CredentialID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var lease remoteruntime.LeasedJob
	if err = json.Unmarshal(backupRuntimeNodeRequest(t, a, node, session.Token, "next", "GET", nil, 200), &lease); err != nil || lease.ID != jobID {
		t.Fatal("manual owner recovery inherited revoked policy authority", err)
	}
	backupRuntimeNodeRequest(t, a, node, session.Token, jobID+"/heartbeat", "POST", remoteruntime.Heartbeat{LeaseToken: lease.LeaseToken}, 200)
}
