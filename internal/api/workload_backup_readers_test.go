package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/backupoperations"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

// This wrapper exposes only the reads used by source and authority checks.
// Credential rotation, service mutation and policy mutation stay unavailable.
type backupReadDomainsStore struct {
	store.Store
	workloadBackupSourceReader
	workloadBackupPolicyReader
	workloadBackupActorReader
	principalGrantReader
	infrastructureAssignmentReader
}

type backupBaseReadStore struct{ store.Store }

func TestBackupExecutionUsesOnlyOutcomeRecords(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	data := a.store
	admission := a.backupAdmission()
	admission.Records = data.(store.WorkloadBackupStore)
	op, err := admission.Manual(ctx, backupoperations.ManualCapture{ID: "minimal-outcome-records", SourceRunID: source.RunID, ServerID: source.Target.ServerID})
	if err != nil {
		t.Fatal(err)
	}
	resultStore := struct {
		store.Store
		workloadBackupExecutionRecords
	}{Store: data, workloadBackupExecutionRecords: data.(workloadBackupExecutionRecords)}
	if _, ok := any(&resultStore).(store.WorkloadBackupStore); ok {
		t.Fatal("wrapper unexpectedly provides backup admission or recovery claims")
	}
	a.store = &resultStore
	dispatched := 0
	a.workloadBackupBackend = func(_ context.Context, input core.WorkloadBackupRequest, server core.Server) (core.WorkloadBackupResult, error) {
		dispatched++
		if input.OperationID != op.ID || input.Key == "" || server.ID != source.Target.ServerID {
			t.Error("execution lost accepted identity or encrypted recovery input")
		}
		return core.WorkloadBackupResult{State: "ready", CleanupState: "complete", Checksum: "authenticated-checksum"}, nil
	}
	a.executeWorkloadBackupOperation(op, false)
	saved, err := data.(store.WorkloadBackupStore).GetWorkloadBackupOperation(ctx, op.ID)
	if err != nil || saved.State != "succeeded" || saved.EncryptedInput != op.EncryptedInput || dispatched != 1 {
		t.Fatal("minimal outcome records did not persist execution", err, dispatched)
	}
}

func TestBackupAuthorityAndSourceUseOnlyConsumerReads(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	var account core.ServiceAccount
	response := automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts", map[string]any{"name": "reader-policy"}, 201)
	if err := json.Unmarshal(response.Body.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	var issued struct {
		Token string `json:"token"`
	}
	response = automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts/"+account.ID+"/credentials", map[string]any{"name": "reader-policy", "expiresAt": time.Now().Add(time.Hour)}, 201)
	if err := json.Unmarshal(response.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	grant := core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: source.ProjectID, Permissions: []core.Permission{core.PermissionProjectView, core.PermissionProjectConfigure, core.PermissionDeploymentRun}}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+source.ProjectID, map[string]any{"kind": "target", "resourceId": source.Target.ServerID}, 200)
	response = mutationRequest(a, issued.Token, "POST", "/api/v1/workload-backup-policies", "reader-policy", map[string]any{"name": "reader-policy", "sourceRunId": source.RunID, "intervalHours": 1, "keepLast": 1, "confirmRetention": "reader-policy"})
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	data := a.store
	policy, err := data.(workloadBackupPolicyReader).GetWorkloadBackupPolicy(ctx, decodeMutation(t, response).OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Actor.Kind != core.PrincipalServiceAccount {
		t.Fatal("fixture did not retain automation actor")
	}
	readers := &backupReadDomainsStore{Store: data,
		workloadBackupSourceReader:     data.(workloadBackupSourceReader),
		workloadBackupPolicyReader:     data.(workloadBackupPolicyReader),
		workloadBackupActorReader:      data.(workloadBackupActorReader),
		principalGrantReader:           data.(principalGrantReader),
		infrastructureAssignmentReader: data.(infrastructureAssignmentReader),
	}
	if _, ok := any(readers).(store.AutomationStore); ok {
		t.Fatal("wrapper unexpectedly provides automation mutations")
	}
	if _, ok := any(readers).(store.WorkloadBackupPolicyStore); ok {
		t.Fatal("wrapper unexpectedly provides policy mutations")
	}
	if _, ok := any(readers).(store.ServiceResourceStore); ok {
		t.Fatal("wrapper unexpectedly provides service mutations")
	}
	a.store = readers
	if err = a.backupOperationPolicyAuthority(ctx, core.WorkloadBackupOperation{CapturePolicyID: policy.ID}); err != nil {
		t.Fatal("read-only adapters rejected current policy authority", err)
	}
	if err = a.deploy.Storage.WithTarget(ctx, policy.ServerID, func() error {
		fresh, accepted, server, storage, err := a.backupSource(ctx, source.RunID)
		if err == nil && (fresh.RunID != source.RunID || accepted.Request.Run.ID != source.RunID || accepted.Request.Password == "" || server.ID != policy.ServerID || storage.Identity != "owned-volume") {
			t.Error("source adapter lost authenticated request or fresh storage")
		}
		return err
	}); err != nil {
		t.Fatal("read-only source adapter rejected source", err)
	}
	if err = data.(store.AutomationStore).RevokeAutomationCredential(ctx, policy.Actor.ID, policy.Actor.CredentialID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err = a.backupOperationPolicyAuthority(ctx, core.WorkloadBackupOperation{CapturePolicyID: policy.ID}); !errors.Is(err, store.ErrAutomationCredential) {
		t.Fatal("read-only actor adapter ignored credential revocation", err)
	}
}

func TestBackupConsumerReadsPreserveUnavailableAndLookupErrors(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	data := a.store
	a.store = &backupBaseReadStore{Store: data}
	if _, err := a.backupPolicyReader(); err == nil || err.Error() != "durable capture policies are unavailable" {
		t.Fatal("changed unavailable policy error", err)
	}
	if err := a.backupOperationPolicyAuthority(ctx, core.WorkloadBackupOperation{}); err != nil {
		t.Fatal("manual operation unexpectedly required policy reads", err)
	}
	if err := a.backupPolicyAuthority(ctx, core.WorkloadBackupPolicy{Actor: core.Identity{Kind: core.PrincipalServiceAccount, ID: "actor"}}); !errors.Is(err, store.ErrAutomationCredential) {
		t.Fatal("changed unavailable automation error", err)
	}
	identity := core.Identity{Kind: core.PrincipalServiceAccount, ID: "actor"}
	if grants, err := a.directProjectGrants(ctx, identity); err != nil || grants != nil {
		t.Fatal("changed unavailable grant read fallback", err)
	}
	if assigned, err := a.assignedInfrastructure(withIdentity(ctx, identity), source.ProjectID, "backup_store", "store"); err != nil || assigned {
		t.Fatal("changed unavailable infrastructure assignment fallback", err)
	}
	if _, err := a.assignedInfrastructure(withIdentity(ctx, identity), source.ProjectID, "target", "missing-target"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("missing target no longer precedes assignment capability fallback", err)
	}
	readerOnly := struct {
		store.Store
		workloadBackupPolicyReader
	}{Store: data, workloadBackupPolicyReader: data.(workloadBackupPolicyReader)}
	a.store = &readerOnly
	if err := a.backupOperationPolicyAuthority(ctx, core.WorkloadBackupOperation{CapturePolicyID: "missing-policy"}); !errors.Is(err, store.ErrWorkloadBackupChanged) {
		t.Fatal("missing policy changed authority error precedence", err)
	}
}
