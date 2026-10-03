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
)

func queuedBackupPolicyRuntime(t *testing.T) (*API, core.PrivateNetwork, edge.Session, core.WorkloadBackupPolicy, core.WorkloadBackupRequest) {
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
	accepted := mutationRequest(a, "secret", "POST", "/api/v1/workload-backup-policies", "runtime-policy", input)
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
