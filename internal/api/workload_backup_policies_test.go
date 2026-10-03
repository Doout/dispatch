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
	"github.com/doout/dispatch/internal/store"
)

func TestBackupCapturePoliciesCreateFreshArchivesAndKeepLastUsable(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	var mu sync.Mutex
	failCapture := false
	captures := map[string]int{}
	deletes := map[string]int{}
	var key string
	a.workloadBackupBackend = func(_ context.Context, input core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
		mu.Lock()
		defer mu.Unlock()
		key = input.Key
		result := core.WorkloadBackupResult{BackupID: input.Backup.ID, ProjectID: input.Backup.ProjectID, OperationID: input.OperationID, ArtifactID: input.Backup.ID, Checksum: strings.Repeat("a", 64), Bytes: 42, CleanupState: "complete"}
		switch input.Action {
		case "backup":
			captures[input.Backup.ID]++
			if failCapture {
				return result, errors.New("fixture capture failed")
			}
			result.State = "ready"
		case "verify":
			result.State = "verified"
		case "delete":
			deletes[input.Backup.ID]++
			result.State = "deleted"
		}
		return result, nil
	}
	input := map[string]any{"name": "daily-pg", "sourceRunId": source.RunID, "intervalHours": 1, "keepLast": 1, "confirmRetention": "daily-pg", "checks": []core.BackupIntegrityCheck{{Query: "SELECT 1", Expected: "1"}}}
	serviceRequestTest(t, a, "POST", "/api/v1/workload-backup-policies", input, 422)
	first := mutationRequest(a, "secret", "POST", "/api/v1/workload-backup-policies", "daily-pg-policy", input)
	if first.Code != 201 {
		t.Fatal(first.Code, first.Body.String())
	}
	receipt := decodeMutation(t, first)
	replay := mutationRequest(a, "secret", "POST", "/api/v1/workload-backup-policies", "daily-pg-policy", input)
	if replay.Code != 202 || decodeMutation(t, replay).OperationID != receipt.OperationID {
		t.Fatal("policy acceptance was not replayed", replay.Code, replay.Body.String())
	}
	policies := a.store.(store.WorkloadBackupPolicyStore)
	backups := a.store.(store.WorkloadBackupStore)
	p, err := policies.GetWorkloadBackupPolicy(ctx, receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.scheduleWorkloadBackupCaptures(ctx) }()
	}
	wg.Wait()
	p, _ = policies.GetWorkloadBackupPolicy(ctx, p.ID)
	if p.LastBackupID == "" {
		t.Fatal("policy accepted no fresh capture")
	}
	original := p.LastBackupID
	awaitBackupOperation(t, a, original, "succeeded")
	a.scheduleWorkloadBackupVerification(ctx)
	operations, _ := backups.ListWorkloadBackupOperations(ctx, original)
	var verification string
	for _, op := range operations {
		if op.Action == "verify" {
			verification = op.ID
		}
	}
	if verification == "" {
		t.Fatal("fresh archive was not scheduled for immediate verification")
	}
	awaitBackupOperation(t, a, verification, "succeeded")
	a.scheduleWorkloadBackupCaptures(ctx)
	p, _ = policies.GetWorkloadBackupPolicy(ctx, p.ID)
	if p.LastVerifiedBackupID != original || p.LastSuccessAt == nil {
		t.Fatal("verified recovery point missing")
	}
	raw := serviceRequestTest(t, a, "GET", "/api/v1/workload-backup-policies/"+p.ID, nil, 200)
	mu.Lock()
	leaked := strings.Contains(string(raw), key)
	mu.Unlock()
	if leaked || strings.Contains(string(raw), "SELECT 1") || strings.Contains(string(raw), p.EncryptedInput) {
		t.Fatal("policy exposed encrypted source inputs")
	}
	// A new capture has its own archive and cannot discard the previous recovery point on failure.
	p.NextCaptureAt = time.Now().UTC().Add(-3 * time.Hour)
	if err = policies.UpdateWorkloadBackupPolicy(ctx, p, p.Revision); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	failCapture = true
	mu.Unlock()
	a.scheduleWorkloadBackupCaptures(ctx)
	p, _ = policies.GetWorkloadBackupPolicy(ctx, p.ID)
	failed := p.LastBackupID
	if failed == original {
		t.Fatal("scheduled capture reused old archive")
	}
	awaitBackupOperation(t, a, failed, "failed")
	a.scheduleWorkloadBackupCaptures(ctx)
	p, _ = policies.GetWorkloadBackupPolicy(ctx, p.ID)
	previous, _ := backups.GetWorkloadBackup(ctx, original)
	if p.State != "capture_failed" || p.MissedCaptures < 2 || previous.State != "ready" || p.LastVerifiedBackupID != original {
		t.Fatal("failed capture lost recovery evidence", p.State, p.MissedCaptures, previous.State)
	}
	mu.Lock()
	if captures[original] != 1 || captures[failed] != 1 || len(deletes) != 0 {
		t.Fatal("duplicate capture or deletion after failure", captures, deletes)
	}
	failCapture = false
	mu.Unlock()
	// A later verified archive makes only the older policy archive eligible for retention.
	p.NextCaptureAt = time.Now().UTC().Add(-time.Second)
	if err = policies.UpdateWorkloadBackupPolicy(ctx, p, p.Revision); err != nil {
		t.Fatal(err)
	}
	a.scheduleWorkloadBackupCaptures(ctx)
	p, _ = policies.GetWorkloadBackupPolicy(ctx, p.ID)
	fresh := p.LastBackupID
	awaitBackupOperation(t, a, fresh, "succeeded")
	a.scheduleWorkloadBackupVerification(ctx)
	operations, _ = backups.ListWorkloadBackupOperations(ctx, fresh)
	for _, op := range operations {
		if op.Action == "verify" {
			awaitBackupOperation(t, a, op.ID, "succeeded")
		}
	}
	a.scheduleWorkloadBackupCaptures(ctx)
	operations, _ = backups.ListWorkloadBackupOperations(ctx, original)
	var deletion string
	for _, op := range operations {
		if op.Action == "delete" {
			deletion = op.ID
		}
	}
	if deletion == "" {
		t.Fatal("retention did not delete older verified archive")
	}
	awaitBackupOperation(t, a, deletion, "succeeded")
	freshBackup, _ := backups.GetWorkloadBackup(ctx, fresh)
	if freshBackup.State != "ready" || freshBackup.VerificationState != "verified" {
		t.Fatal("retention removed the last usable archive")
	}
	p, _ = policies.GetWorkloadBackupPolicy(ctx, p.ID)
	serviceRequestTest(t, a, "PUT", "/api/v1/workload-backup-policies/"+p.ID, map[string]any{"revision": p.Revision, "confirmName": p.Name}, 422)
	serviceRequestTest(t, a, "PUT", "/api/v1/workload-backup-policies/"+p.ID, map[string]any{"revision": p.Revision, "enabled": false, "confirmName": p.Name}, 200)
	a.scheduleWorkloadBackupCaptures(ctx)
	mu.Lock()
	defer mu.Unlock()
	if captures[fresh] != 1 || deletes[original] != 1 {
		t.Fatal("retention or capture repeated", captures, deletes)
	}
}
func TestBackupCapturePolicyRejectsStaleStorageAndRevokedActor(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	token, _ := offsiteIdentity(t, a, source.ProjectID)
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+source.ProjectID, map[string]any{"kind": "target", "resourceId": source.Target.ServerID}, 200)
	calls := 0
	a.workloadBackupBackend = func(_ context.Context, _ core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
		calls++
		return core.WorkloadBackupResult{}, nil
	}
	input := map[string]any{"name": "owned-capture", "sourceRunId": source.RunID, "intervalHours": 1, "keepLast": 1, "confirmRetention": "owned-capture"}
	response := mutationRequest(a, token, "POST", "/api/v1/workload-backup-policies", "ownership-policy", input)
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	receipt := decodeMutation(t, response)
	policies := a.store.(store.WorkloadBackupPolicyStore)
	p, _ := policies.GetWorkloadBackupPolicy(ctx, receipt.OperationID)
	// Inventory labels still match, but a replacement volume identity must not be adopted.
	a.deploy.Storage.Backend.(*storageTestBackend).observation.Resource.Identity = "replacement-volume"
	a.scheduleWorkloadBackupCaptures(ctx)
	p, _ = policies.GetWorkloadBackupPolicy(ctx, p.ID)
	if calls != 0 || p.State != "blocked" || p.MissedCaptures != 1 || p.LastBackupID != "" {
		t.Fatal("stale volume identity allowed mutation", calls, p.State, p.MissedCaptures)
	}
	if err := a.store.(store.AutomationStore).RevokeAutomationCredential(ctx, p.Actor.ID, p.Actor.CredentialID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.NextCaptureAt = time.Now().Add(-time.Second)
	if err := policies.UpdateWorkloadBackupPolicy(ctx, p, p.Revision); err != nil {
		t.Fatal(err)
	}
	a.scheduleWorkloadBackupCaptures(ctx)
	p, _ = policies.GetWorkloadBackupPolicy(ctx, p.ID)
	if calls != 0 || p.MissedCaptures != 2 {
		t.Fatal("removed actor authorized unattended capture")
	}
	raw := serviceRequestTest(t, a, "GET", "/api/v1/workload-backup-policies", nil, 200)
	var visible []core.WorkloadBackupPolicy
	if err := json.Unmarshal(raw, &visible); err != nil || len(visible) != 1 {
		t.Fatal("policy inventory missing", err)
	}
}

func TestBackupCapturePolicyLostReplyRecoversOriginalArchive(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	var mu sync.Mutex
	mutations := 0
	inspections := 0
	a.workloadBackupBackend = func(_ context.Context, input core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
		mu.Lock()
		defer mu.Unlock()
		result := core.WorkloadBackupResult{BackupID: input.Backup.ID, ProjectID: input.Backup.ProjectID, OperationID: input.OperationID, ArtifactID: input.Backup.ID, CleanupState: "complete"}
		if input.Action == "backup" {
			mutations++
			result.State = "unknown"
			return result, errors.New("fixture lost reply")
		}
		if input.Action == "reconcile" {
			inspections++
			if input.RecoveryAction != "backup" {
				t.Error("reconciliation discarded original action")
			}
			result.State = "ready"
			result.Checksum = strings.Repeat("a", 64)
			return result, nil
		}
		return result, nil
	}
	response := mutationRequest(a, "secret", "POST", "/api/v1/workload-backup-policies", "lost-capture-policy", map[string]any{"name": "lost-capture", "sourceRunId": source.RunID, "intervalHours": 1, "keepLast": 1, "confirmRetention": "lost-capture"})
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	policies := a.store.(store.WorkloadBackupPolicyStore)
	backups := a.store.(store.WorkloadBackupStore)
	p, _ := policies.GetWorkloadBackupPolicy(ctx, decodeMutation(t, response).OperationID)
	a.scheduleWorkloadBackupCaptures(ctx)
	p, _ = policies.GetWorkloadBackupPolicy(ctx, p.ID)
	original := p.LastBackupID
	awaitBackupOperation(t, a, original, "unknown")
	p.NextCaptureAt = time.Now().Add(-time.Second)
	if err := policies.UpdateWorkloadBackupPolicy(ctx, p, p.Revision); err != nil {
		t.Fatal(err)
	}
	a.scheduleWorkloadBackupCaptures(ctx)
	p, _ = policies.GetWorkloadBackupPolicy(ctx, p.ID)
	if p.LastBackupID != original || p.State != "blocked" {
		t.Fatal("uncertain capture was replaced")
	}
	// Advance only the recovery claim's observation time to simulate lease expiry.
	recovered, err := backups.ClaimWorkloadBackupRecovery(ctx, original, time.Now().Add(32*time.Minute), "replacement-controller")
	if err != nil {
		t.Fatal(err)
	}
	a.executeWorkloadBackupOperation(recovered, true)
	awaitBackupOperation(t, a, original, "succeeded")
	saved, _ := backups.GetWorkloadBackup(ctx, original)
	if saved.State != "ready" {
		t.Fatal("original archive not adopted after inspection")
	}
	mu.Lock()
	defer mu.Unlock()
	if mutations != 1 || inspections != 1 {
		t.Fatal("recovery replayed an uncertain dump", mutations, inspections)
	}
}

func TestBackupCapturePolicyAutomationRevocationStopsUnattendedWork(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	calls := 0
	a.workloadBackupBackend = func(_ context.Context, _ core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
		calls++
		return core.WorkloadBackupResult{}, nil
	}
	var account core.ServiceAccount
	response := automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts", map[string]any{"name": "backup-captures"}, 201)
	json.Unmarshal(response.Body.Bytes(), &account)
	var issued struct {
		Credential core.AutomationCredential `json:"credential"`
		Token      string                    `json:"token"`
	}
	credentialPath := "/api/v1/automation-accounts/" + account.ID + "/credentials"
	response = automationRequest(t, a, "secret", "POST", credentialPath, map[string]any{"name": "capture-policy", "expiresAt": time.Now().Add(time.Hour)}, 201)
	json.Unmarshal(response.Body.Bytes(), &issued)
	grant := core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: source.ProjectID, Permissions: []core.Permission{core.PermissionProjectView}}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	input := map[string]any{"name": "automation-capture", "sourceRunId": source.RunID, "intervalHours": 1, "keepLast": 1, "confirmRetention": "automation-capture"}
	denied := mutationRequest(a, issued.Token, "POST", "/api/v1/workload-backup-policies", "scoped-capture-policy", input)
	if denied.Code != 403 {
		t.Fatal("view grant accepted capture mutation", denied.Code)
	}
	grant.Permissions = append(grant.Permissions, core.PermissionProjectConfigure, core.PermissionDeploymentRun)
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+source.ProjectID, map[string]any{"kind": "target", "resourceId": source.Target.ServerID}, 200)
	accepted := mutationRequest(a, issued.Token, "POST", "/api/v1/workload-backup-policies", "scoped-capture-policy", input)
	if accepted.Code != 201 {
		t.Fatal(accepted.Code, accepted.Body.String())
	}
	receipt := decodeMutation(t, accepted)
	policy, _ := a.store.(store.WorkloadBackupPolicyStore).GetWorkloadBackupPolicy(ctx, receipt.OperationID)
	request, err := a.decryptWorkloadBackup(policy.ID, "policy", policy.EncryptedInput)
	if err != nil {
		t.Fatal(err)
	}
	queued := request.Backup
	queued.ID = "policy-queued-backup"
	queued.ArtifactID = queued.ID
	queued.State = "creating"
	queued.Revision = 1
	queued.CreatedAt = time.Now().UTC()
	queued.UpdatedAt = queued.CreatedAt
	request.Backup = queued
	request.OperationID = queued.ID
	queued.EncryptedInput, err = a.encryptWorkloadBackup(queued.ID, "accepted", request)
	if err != nil {
		t.Fatal(err)
	}
	op := newBackupOperation(queued.ID, queued, "backup")
	op.CapturePolicyID = policy.ID
	op.EncryptedInput, err = a.encryptWorkloadBackup(op.ID, "operation", request)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.store.(store.WorkloadBackupStore).CreateWorkloadBackup(ctx, queued, op); err != nil {
		t.Fatal(err)
	}
	if err = a.checkBackupPolicyRuntimeAuthority(ctx, "backup-"+op.ID); err != nil {
		t.Fatal("current credential rejected queued policy job", err)
	}
	automationRequest(t, a, "secret", "POST", credentialPath+"/"+issued.Credential.ID+"/revoke", nil, 204)
	if err = a.checkBackupPolicyRuntimeAuthority(ctx, "backup-"+op.ID); err == nil {
		t.Fatal("revoked credential released queued agent inputs")
	}
	a.scheduleWorkloadBackupCaptures(ctx)
	p, err := a.store.(store.WorkloadBackupPolicyStore).GetWorkloadBackupPolicy(ctx, receipt.OperationID)
	if err != nil || calls != 0 || p.State != "blocked" || p.LastBackupID != "" {
		t.Fatal("revoked automation credential performed capture", err, calls, p.State)
	}
}

type backupExecutionTargetStore struct {
	*store.SQLStore
	targetID        string
	mu              sync.Mutex
	reads           int
	executionLookup chan struct{}
}

func (s *backupExecutionTargetStore) GetServer(ctx context.Context, id string) (core.Server, error) {
	server, err := s.SQLStore.GetServer(ctx, id)
	if id == s.targetID {
		s.mu.Lock()
		s.reads++
		// The first read validates policy authority. The second prepares dispatch
		// after that check has passed and before waiting for the target lock.
		if s.reads == 2 {
			close(s.executionLookup)
		}
		s.mu.Unlock()
	}
	return server, err
}

func TestBackupCapturePolicyPauseWhileWaitingForTargetStopsDispatch(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	response := mutationRequest(a, "secret", "POST", "/api/v1/workload-backup-policies", "queued-capture-policy", map[string]any{"name": "queued-capture", "sourceRunId": source.RunID, "intervalHours": 1, "keepLast": 1, "confirmRetention": "queued-capture"})
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	policies := a.store.(store.WorkloadBackupPolicyStore)
	backups := a.store.(store.WorkloadBackupStore)
	p, err := policies.GetWorkloadBackupPolicy(ctx, decodeMutation(t, response).OperationID)
	if err != nil {
		t.Fatal(err)
	}
	request, err := a.decryptWorkloadBackup(p.ID, "policy", p.EncryptedInput)
	if err != nil {
		t.Fatal(err)
	}
	b := request.Backup
	b.ID, b.ArtifactID, b.CapturePolicyID = "queued-policy-capture", "queued-policy-capture", p.ID
	b.State, b.Revision = "creating", 1
	b.CreatedAt, b.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	request.Backup, request.OperationID = b, b.ID
	b.EncryptedInput, err = a.encryptWorkloadBackup(b.ID, "accepted", request)
	if err != nil {
		t.Fatal(err)
	}
	op := newBackupOperation(b.ID, b, "backup")
	op.CapturePolicyID = p.ID
	op.EncryptedInput, err = a.encryptWorkloadBackup(op.ID, "operation", request)
	if err != nil {
		t.Fatal(err)
	}
	if err = backups.CreateWorkloadBackup(ctx, b, op); err != nil {
		t.Fatal(err)
	}
	data := &backupExecutionTargetStore{SQLStore: a.store.(*store.SQLStore), targetID: p.ServerID, executionLookup: make(chan struct{})}
	a.store = data
	dispatched := make(chan struct{}, 1)
	a.workloadBackupBackend = func(_ context.Context, _ core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
		dispatched <- struct{}{}
		return core.WorkloadBackupResult{State: "ready", CleanupState: "complete"}, nil
	}
	locked, release, unlocked := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseTarget := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseTarget)
	go func() {
		defer close(unlocked)
		_ = a.deploy.Storage.WithTarget(ctx, p.ServerID, func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("target lock was not acquired")
	}
	finished := make(chan struct{})
	go func() { defer close(finished); a.executeWorkloadBackupOperation(op, false) }()
	select {
	case <-data.executionLookup:
	case <-time.After(5 * time.Second):
		t.Fatal("accepted policy authority did not pass before dispatch preparation")
	}
	// Pausing after the first authority check must still stop queued execution.
	p.Enabled = false
	if err = policies.UpdateWorkloadBackupPolicy(ctx, p, p.Revision); err != nil {
		t.Fatal(err)
	}
	releaseTarget()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("queued capture did not finish after releasing the target lock")
	}
	<-unlocked
	select {
	case <-dispatched:
		t.Fatal("paused policy dispatched a backup after waiting for the target lock")
	default:
	}
	saved, err := backups.GetWorkloadBackupOperation(ctx, op.ID)
	if err != nil || saved.State != "failed" || saved.CleanupState != "complete" {
		t.Fatal("undispatched capture lost its confirmed cleanup outcome", saved.State, saved.CleanupState, err)
	}
	retained, err := backups.GetWorkloadBackup(ctx, b.ID)
	if err != nil || retained.State != "failed" {
		t.Fatal("undispatched capture was reported as a recovery point", retained.State, err)
	}
}
