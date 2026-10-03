package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
)

// Expose the base store and the backup domains without promoting SQLStore's
// credential methods. This also models a store without enrollment support.
type backupEnrollmentTestDomains struct {
	store.Store
	store.ServiceResourceStore
	store.WorkloadBackupPolicyStore
	store.WorkloadBackupStore
	store.MutationReceiptStore
	store.AutomationStore
	store.BackupObjectStoreStore
}

func backupEnrollmentDomains(data store.Store) *backupEnrollmentTestDomains {
	return &backupEnrollmentTestDomains{
		Store: data, ServiceResourceStore: data.(store.ServiceResourceStore),
		WorkloadBackupPolicyStore: data.(store.WorkloadBackupPolicyStore),
		WorkloadBackupStore:       data.(store.WorkloadBackupStore),
		MutationReceiptStore:      data.(store.MutationReceiptStore),
		AutomationStore:           data.(store.AutomationStore),
		BackupObjectStoreStore:    data.(store.BackupObjectStoreStore),
	}
}

type backupEnrollmentTestStore struct {
	*backupEnrollmentTestDomains
	read func(context.Context, string) (core.EdgeCredential, error)
}

func (s *backupEnrollmentTestStore) GetEdgeCredential(ctx context.Context, id string) (core.EdgeCredential, error) {
	return s.read(ctx, id)
}

type backupRuntimeEnrollmentTestStore struct {
	*backupEnrollmentTestStore
	store.RuntimeJobStore
}

func (s *backupRuntimeEnrollmentTestStore) GetEdgeCredential(ctx context.Context, id string) (core.EdgeCredential, error) {
	return s.backupEnrollmentTestStore.GetEdgeCredential(ctx, id)
}

func enrolledBackupAPIFixture(t *testing.T) (*API, core.ServiceResource, core.EdgeCredential) {
	t.Helper()
	a, _, template := resourceAPIFixture(t)
	ctx := context.Background()
	var node core.PrivateNetwork
	if err := json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/private-networks", map[string]string{"name": "wrapped-backup", "driver": "dispatch_agent"}, 201), &node); err != nil {
		t.Fatal(err)
	}
	enrollNodeTest(t, a, node)
	server, err := a.store.GetServer(ctx, "resource-target")
	if err != nil {
		t.Fatal(err)
	}
	server.AgentNodeID = node.ID
	if err = a.store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	var run core.ServiceProvisionRun
	if err = json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/service-templates/"+template.ID+"/runs", map[string]any{"name": "wrapped-backup-db"}, 202), &run); err != nil {
		t.Fatal(err)
	}
	source := awaitServiceResource(t, a, run.ID, "ready")
	a.deploy.Storage.Backend = &storageTestBackend{observation: core.StorageObservation{Resource: core.StorageResource{Kind: "docker_volume", Name: deploy.ServiceResourceName(run.ID) + "-data", Identity: "owned-volume", Evidence: "labels"}, Labels: map[string]string{"dispatch.managed-by": "dispatch", "dispatch.project": source.ProjectID, "dispatch.service-template": template.ID, "dispatch.service-provision": run.ID}}}
	credential, err := a.store.(workloadBackupEnrollmentReader).GetEdgeCredential(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a, source, credential
}

func TestBackupEnrollmentAdmissionWithWrappedStore(t *testing.T) {
	a, source, credential := enrolledBackupAPIFixture(t)
	ctx := context.Background()
	data := a.store
	domains := backupEnrollmentDomains(data)
	input := map[string]any{"name": "wrapped-policy", "sourceRunId": source.RunID, "intervalHours": 1, "keepLast": 1, "confirmRetention": "wrapped-policy"}
	for _, tc := range []struct {
		name       string
		credential core.EdgeCredential
		err        error
		absent     bool
	}{
		{name: "absent", absent: true},
		{name: "read-error", credential: credential, err: errors.New("enrollment read failed")},
		{name: "revoked", credential: core.EdgeCredential{NetworkID: credential.NetworkID, Generation: credential.Generation, PublicKey: credential.PublicKey, Revoked: true}},
		{name: "missing-public-key", credential: core.EdgeCredential{NetworkID: credential.NetworkID, Generation: credential.Generation}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a.store = domains
			if !tc.absent {
				a.store = &backupEnrollmentTestStore{backupEnrollmentTestDomains: domains, read: func(context.Context, string) (core.EdgeCredential, error) { return tc.credential, tc.err }}
			}
			response := mutationRequest(a, "secret", "POST", "/api/v1/workload-backup-policies", "wrapped-policy-"+tc.name, input)
			if response.Code != 409 {
				t.Fatal("invalid enrollment accepted policy", response.Code, response.Body.String())
			}
			policies, err := domains.ListWorkloadBackupPolicies(ctx, source.ProjectID)
			if err != nil || len(policies) != 0 {
				t.Fatal("rejected enrollment persisted policy", err, len(policies))
			}
		})
	}
	var reads atomic.Int64
	a.store = &backupEnrollmentTestStore{backupEnrollmentTestDomains: domains, read: func(ctx context.Context, id string) (core.EdgeCredential, error) {
		reads.Add(1)
		return data.(workloadBackupEnrollmentReader).GetEdgeCredential(ctx, id)
	}}
	response := mutationRequest(a, "secret", "POST", "/api/v1/workload-backup-policies", "wrapped-policy-current", input)
	if response.Code != 201 {
		t.Fatal("current enrollment through wrapper rejected", response.Code, response.Body.String())
	}
	policy, err := domains.GetWorkloadBackupPolicy(ctx, decodeMutation(t, response).OperationID)
	if err != nil || policy.NodeID != credential.NetworkID || policy.NodeGeneration != credential.Generation || reads.Load() != 2 {
		t.Fatal("policy did not freeze and recheck enrolled identity", err, policy.NodeID, policy.NodeGeneration, reads.Load())
	}
	if err = a.backupPolicyTarget(ctx, policy); err != nil {
		t.Fatal("wrapped current policy target rejected", err)
	}
}

func TestBackupEnrollmentAuthorityWithWrappedRuntimeStore(t *testing.T) {
	a, _, _, policy, request := queuedBackupPolicyRuntimeForActor(t, true)
	ctx := context.Background()
	data := a.store
	domains := backupEnrollmentDomains(data)
	credential, err := data.(workloadBackupEnrollmentReader).GetEdgeCredential(ctx, policy.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	op := core.WorkloadBackupOperation{ExecutionNodeID: policy.NodeID, ExecutionGeneration: policy.NodeGeneration}
	for _, tc := range []struct {
		name       string
		credential core.EdgeCredential
		err        error
		valid      bool
	}{
		{name: "current", credential: credential, valid: true},
		{name: "read-error", credential: credential, err: errors.New("enrollment read failed")},
		{name: "revoked", credential: core.EdgeCredential{Generation: credential.Generation, PublicKey: credential.PublicKey, Revoked: true}},
		{name: "missing-public-key", credential: core.EdgeCredential{Generation: credential.Generation}},
		{name: "generation-changed", credential: core.EdgeCredential{Generation: credential.Generation + 1, PublicKey: credential.PublicKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a.store = &backupRuntimeEnrollmentTestStore{
				backupEnrollmentTestStore: &backupEnrollmentTestStore{backupEnrollmentTestDomains: domains, read: func(context.Context, string) (core.EdgeCredential, error) { return tc.credential, tc.err }},
				RuntimeJobStore:           data.(store.RuntimeJobStore),
			}
			for name, check := range map[string]func() error{
				"policy":  func() error { return a.backupPolicyTarget(ctx, policy) },
				"runtime": func() error { return a.checkBackupPolicyRuntimeAuthority(ctx, "backup-"+request.OperationID) },
			} {
				err := check()
				if tc.valid && err != nil || !tc.valid && !errors.Is(err, store.ErrWorkloadBackupChanged) {
					t.Fatalf("%s authority: valid=%v error=%v", name, tc.valid, err)
				}
			}
			if got := a.backupExecutionGenerationMatches(ctx, op); got != tc.valid {
				t.Fatalf("execution enrollment valid=%v, want %v", got, tc.valid)
			}
		})
	}
	a.store = domains
	if err = a.backupPolicyTarget(ctx, policy); !errors.Is(err, store.ErrWorkloadBackupChanged) || a.backupExecutionGenerationMatches(ctx, op) {
		t.Fatal("absent enrollment capability authorized bound target", err)
	}
	if err = a.backupPolicyTarget(ctx, core.WorkloadBackupPolicy{ServerID: "missing-target", NodeID: policy.NodeID}); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("absent enrollment capability changed target lookup error precedence", err)
	}
	if !a.backupExecutionGenerationMatches(ctx, core.WorkloadBackupOperation{}) {
		t.Fatal("local execution unexpectedly requires enrollment capability")
	}
}

func TestBackupExportEnrollmentWithWrappedStore(t *testing.T) {
	a, source, credential := enrolledBackupAPIFixture(t)
	ctx := context.Background()
	data := a.store
	domains := backupEnrollmentDomains(data)
	var destination core.BackupObjectStore
	if err := json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/workload-backup-stores", offsiteRegistration(t, a, source.ProjectID, "wrapped-export"), 201), &destination); err != nil {
		t.Fatal(err)
	}
	var exports atomic.Int64
	a.workloadBackupBackend = func(_ context.Context, input core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
		if input.Action == "export" {
			exports.Add(1)
		}
		return scheduledOffsiteResult(input), nil
	}
	capture := mutationRequest(a, "secret", "POST", "/api/v1/workload-backups", "wrapped-export-capture", map[string]any{"sourceRunId": source.RunID})
	if capture.Code != 202 {
		t.Fatal(capture.Code, capture.Body.String())
	}
	backupID := decodeMutation(t, capture).OperationID
	awaitBackupOperation(t, a, backupID, "succeeded")
	path := "/api/v1/workload-backups/" + backupID + "/export"
	for _, tc := range []struct {
		name       string
		credential core.EdgeCredential
		err        error
		absent     bool
		status     int
	}{
		{name: "absent", absent: true, status: 500},
		{name: "read-error", credential: credential, err: errors.New("enrollment read failed"), status: 409},
		{name: "revoked", credential: core.EdgeCredential{Generation: credential.Generation, PublicKey: credential.PublicKey, Revoked: true}, status: 409},
		{name: "missing-public-key", credential: core.EdgeCredential{Generation: credential.Generation}, status: 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a.store = domains
			if !tc.absent {
				a.store = &backupEnrollmentTestStore{backupEnrollmentTestDomains: domains, read: func(context.Context, string) (core.EdgeCredential, error) { return tc.credential, tc.err }}
			}
			response := mutationRequest(a, "secret", "POST", path, "wrapped-export-"+tc.name, map[string]any{"storeId": destination.ID})
			if response.Code != tc.status {
				t.Fatal("changed export enrollment rejection", response.Code, response.Body.String())
			}
			ops, err := domains.ListWorkloadBackupOperations(ctx, backupID)
			if err != nil || len(ops) != 1 || exports.Load() != 0 {
				t.Fatal("rejected enrollment admitted or dispatched export", err, len(ops), exports.Load())
			}
		})
	}
	a.store = &backupEnrollmentTestStore{backupEnrollmentTestDomains: domains, read: data.(workloadBackupEnrollmentReader).GetEdgeCredential}
	response := mutationRequest(a, "secret", "POST", path, "wrapped-export-current", map[string]any{"storeId": destination.ID})
	if response.Code != 202 {
		t.Fatal("current enrollment through wrapper rejected export", response.Code, response.Body.String())
	}
	op := awaitBackupOperation(t, a, decodeMutation(t, response).OperationID, "succeeded")
	accepted, err := a.decryptWorkloadBackup(op.ID, "operation", op.EncryptedInput)
	if err != nil || op.ExecutionGeneration != credential.Generation || accepted.ExecutionNodeGeneration != credential.Generation || exports.Load() != 1 {
		t.Fatal("export lost accepted enrollment or dispatched twice", err, op.ExecutionGeneration, accepted.ExecutionNodeGeneration, exports.Load())
	}
}
