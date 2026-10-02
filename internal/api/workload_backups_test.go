package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
)

func backupAPIFixture(t *testing.T) (*API, core.ServiceResource) {
	t.Helper()
	a, _, template := resourceAPIFixture(t)
	var run core.ServiceProvisionRun
	json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/service-templates/"+template.ID+"/runs", map[string]any{"name": "backup-db"}, 202), &run)
	resource := awaitServiceResource(t, a, run.ID, "ready")
	a.deploy.Storage.Backend = &storageTestBackend{observation: core.StorageObservation{Resource: core.StorageResource{Kind: "docker_volume", Name: deploy.ServiceResourceName(run.ID) + "-data", Identity: "owned-volume", Evidence: "labels"}, Labels: map[string]string{"dispatch.managed-by": "dispatch", "dispatch.project": resource.ProjectID, "dispatch.service-template": template.ID, "dispatch.service-provision": run.ID}}}
	return a, resource
}
func awaitBackupOperation(t *testing.T, a *API, id, state string) core.WorkloadBackupOperation {
	t.Helper()
	s := a.store.(store.WorkloadBackupStore)
	deadline := time.Now().Add(6 * time.Second)
	var op core.WorkloadBackupOperation
	for time.Now().Before(deadline) {
		op, _ = s.GetWorkloadBackupOperation(context.Background(), id)
		if op.State == state {
			return op
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("backup operation did not reach %s: %+v", state, op)
	return op
}
func TestWorkloadBackupAPIReceiptsConfirmationAndProtectedTarget(t *testing.T) {
	a, source := backupAPIFixture(t)
	var mu sync.Mutex
	calls := map[string]int{}
	var secret string
	a.workloadBackupBackend = func(_ context.Context, input core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
		mu.Lock()
		defer mu.Unlock()
		calls[input.Action]++
		secret = input.Key
		state := map[string]string{"backup": "ready", "verify": "verified", "restore": "restored", "delete": "deleted"}[input.Action]
		return core.WorkloadBackupResult{BackupID: input.Backup.ID, ProjectID: input.Backup.ProjectID, OperationID: input.OperationID, ArtifactID: input.Backup.ID, State: state, Checksum: strings.Repeat("a", 64), PlaintextChecksum: strings.Repeat("b", 64), Bytes: 42, ImageID: "sha256:" + strings.Repeat("c", 64), CleanupState: "complete", Message: "Recorded backup outcome."}, nil
	}
	input := map[string]any{"sourceRunId": source.RunID, "checks": []map[string]string{{"query": "SELECT 1", "expected": "1"}}, "verificationIntervalHours": 24}
	first := mutationRequest(a, "secret", "POST", "/api/v1/workload-backups", "create-database-backup", input)
	if first.Code != 202 {
		t.Fatal(first.Code, first.Body.String())
	}
	receipt := decodeMutation(t, first)
	awaitBackupOperation(t, a, receipt.OperationID, "succeeded")
	replay := mutationRequest(a, "secret", "POST", "/api/v1/workload-backups", "create-database-backup", input)
	if replay.Code != 202 || decodeMutation(t, replay).OperationID != receipt.OperationID {
		t.Fatal("backup retry did not reuse receipt", replay.Body.String())
	}
	base := "/api/v1/workload-backups/" + receipt.OperationID
	s := a.store.(store.WorkloadBackupStore)
	b, err := s.GetWorkloadBackup(context.Background(), receipt.OperationID)
	if err != nil || b.State != "ready" || b.EncryptedInput == "" || b.NextVerificationAt == nil {
		t.Fatal(b, err)
	}
	raw := serviceRequestTest(t, a, "GET", base, nil, 200)
	mu.Lock()
	leaked := strings.Contains(string(raw), secret) || strings.Contains(b.EncryptedInput, secret)
	mu.Unlock()
	if leaked || strings.Contains(string(raw), "SELECT 1") {
		t.Fatal("public backup exposed recovery credentials")
	}
	var op core.WorkloadBackupOperation
	json.Unmarshal(serviceRequestTest(t, a, "POST", base+"/verify", nil, 202), &op)
	awaitBackupOperation(t, a, op.ID, "succeeded")
	// Restoring to the original owned service is explicit and cannot bypass typed review.
	restore := base + "/restore/" + source.RunID
	serviceRequestTest(t, a, "POST", restore, map[string]any{}, 422)
	var review destructiveReview
	json.Unmarshal(serviceRequestTest(t, a, "POST", restore+"/preview", nil, 200), &review)
	confirm := map[string]any{"confirmation": destructiveConfirmation{ResourceID: b.ID, Action: "restore", ExpectedVersion: review.Version, ConfirmName: "wrong"}}
	serviceRequestTest(t, a, "POST", restore, confirm, 422)
	confirm["confirmation"] = destructiveConfirmation{ResourceID: b.ID, Action: "restore", ExpectedVersion: "stale", ConfirmName: source.Name}
	serviceRequestTest(t, a, "POST", restore, confirm, 409)
	confirm["confirmation"] = destructiveConfirmation{ResourceID: b.ID, Action: "restore", ExpectedVersion: review.Version, ConfirmName: source.Name}
	json.Unmarshal(serviceRequestTest(t, a, "POST", restore, confirm, 202), &op)
	awaitBackupOperation(t, a, op.ID, "succeeded")
	if err = a.store.DeleteServer(context.Background(), b.ServerID); err == nil {
		t.Fatal("retained backup did not protect target registration")
	}
	json.Unmarshal(serviceRequestTest(t, a, "POST", base+"/delete-preview", nil, 200), &review)
	confirm["confirmation"] = destructiveConfirmation{ResourceID: b.ID, Action: "delete", ExpectedVersion: review.Version, ConfirmName: b.ID}
	deleted := mutationRequest(a, "secret", "POST", base+"/delete", "delete-backup-archive", confirm)
	if deleted.Code != 202 {
		t.Fatal(deleted.Code, deleted.Body.String())
	}
	deletionReceipt := decodeMutation(t, deleted)
	awaitBackupOperation(t, a, deletionReceipt.OperationID, "succeeded")
	deleted = mutationRequest(a, "secret", "POST", base+"/delete", "delete-backup-archive", confirm)
	if deleted.Code != 202 {
		t.Fatal("completed deletion was not replayed", deleted.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["backup"] != 1 || calls["restore"] != 1 || calls["delete"] != 1 || calls["verify"] != 1 {
		t.Fatal("unexpected repeated side effects", calls)
	}
}
func TestWorkloadBackupInterruptedVerificationPreservesRecoveryLease(t *testing.T) {
	a, source := backupAPIFixture(t)
	a.workloadBackupBackend = func(_ context.Context, input core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
		result := core.WorkloadBackupResult{BackupID: input.Backup.ID, ProjectID: input.Backup.ProjectID, OperationID: input.OperationID, ArtifactID: input.Backup.ID, State: "ready", Checksum: strings.Repeat("a", 64), Bytes: 42, CleanupState: "complete"}
		if input.Action == "verify" {
			result.State, result.CleanupState = "unknown", "failed"
			return result, errors.New("lost cleanup response")
		}
		return result, nil
	}
	var op core.WorkloadBackupOperation
	json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/workload-backups", map[string]any{"sourceRunId": source.RunID}, 202), &op)
	awaitBackupOperation(t, a, op.ID, "succeeded")
	base := "/api/v1/workload-backups/" + op.BackupID
	json.Unmarshal(serviceRequestTest(t, a, "POST", base+"/verify", nil, 202), &op)
	unknown := awaitBackupOperation(t, a, op.ID, "unknown")
	if !unknown.LeaseUntil.After(time.Now()) {
		t.Fatal("uncertain execution lost its safety lease")
	}
	serviceRequestTest(t, a, "POST", base+"/operations/"+op.ID+"/reconcile", nil, 409)
	serviceRequestTest(t, a, "POST", base+"/verify", nil, 409)
}
