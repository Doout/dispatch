package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/doout/dispatch/internal/store"
)

const legacyOffsiteCapabilities = "deploy,inspect,logs,start,stop,rollback,destroy,provision_service,service_inspect,service_delete,workload_backup,workload_backup_inspect,workload_backup_offsite,workload_backup_offsite_inspect,storage_inspect,storage_delete,retention_inspect,retention_prune"
const cacheCleanupCapabilities = legacyOffsiteCapabilities + ",workload_backup_retire,workload_backup_retire_inspect"

func TestBackupCacheCapabilityLimitsBeforeLeaseAndDuringRenewal(t *testing.T) {
	const required = "workload_backup_offsite,workload_backup_retire"
	for _, tc := range []struct {
		name, header string
		valid        bool
	}{
		{"20 entries", required + "," + strings.Repeat("inspect,", 17) + "inspect", true},
		{"21 entries", required + "," + strings.Repeat("inspect,", 18) + "inspect", false},
		{"512 bytes", required + "," + strings.Repeat("x", 512-len(required)-1), true},
		{"513 bytes", required + "," + strings.Repeat("x", 513-len(required)-1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, node, session, request, id := queuedCacheRecovery(t, "retired-source", "verify")
			want := 422
			if tc.valid {
				want = 200
			}
			raw := cacheRuntimeRequest(t, a, node, session.Token, "next", "GET", tc.header, nil, want)
			if !tc.valid {
				job, err := a.runtimeBroker().Store.GetRuntimeJob(context.Background(), id)
				if err != nil || job.State != "pending" || job.LeaseToken != "" || job.EncryptedRequest == "" {
					t.Fatal("invalid capabilities changed the queued request", err)
				}
				raw = cacheRuntimeRequest(t, a, node, session.Token, "next", "GET", cacheCleanupCapabilities, nil, 200)
			}
			var lease remoteruntime.LeasedJob
			if err := json.Unmarshal(raw, &lease); err != nil || lease.ID != id {
				t.Fatal("capability negotiation lost the queued job", err)
			}
			cacheRuntimeRequest(t, a, node, session.Token, id+"/heartbeat", "POST", tc.header, remoteruntime.Heartbeat{LeaseToken: "stale-lease"}, 409)
			cacheRuntimeRequest(t, a, node, session.Token, id+"/heartbeat", "POST", tc.header, remoteruntime.Heartbeat{LeaseToken: lease.LeaseToken}, want)
			job, err := a.runtimeBroker().Store.GetRuntimeJob(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if tc.valid {
				if job.State != "running" || job.LeaseToken != lease.LeaseToken || job.EncryptedRequest == "" {
					t.Fatal("valid capability boundary interrupted execution")
				}
			} else {
				if job.State != "unknown" || !job.CancelRequested || job.LeaseToken != "" || job.EncryptedRequest != "" {
					t.Fatal("invalid cleanup capabilities retained a usable lease")
				}
				op, err := a.store.(store.WorkloadBackupStore).GetWorkloadBackupOperation(context.Background(), request.WorkloadBackup.OperationID)
				if err != nil || op.EncryptedInput == "" {
					t.Fatal("invalid cleanup capabilities erased recovery input", err)
				}
			}
		})
	}
}

func cacheRuntimeRequest(t *testing.T, a *API, node core.PrivateNetwork, token, suffix, method, capabilities string, input any, want int) []byte {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, "/api/v1/edge/nodes/"+node.ID+"/runtime/jobs/"+suffix, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Dispatch-Runtime-Version", remoteruntime.APIVersion)
	r.Header.Set("X-Dispatch-Runtime-Capabilities", capabilities)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatal(method, suffix, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

func ownedCacheRecoverySource(t *testing.T, a *API, project string, server core.Server, id string) core.ServiceProvisionRequest {
	t.Helper()
	ctx, now := context.Background(), time.Now().UTC()
	data := a.store.(store.ServiceResourceStore)
	run := core.ServiceProvisionRun{ID: id, TemplateID: "cache-template", ProjectID: project, ServiceName: id, State: "queued", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: server.ID}, CreatedAt: now}
	resource := core.ServiceResource{RunID: id, ProjectID: project, ServiceID: id + "-service", Name: id, Target: *run.Target, State: "accepted", Policy: "retain", Revision: 1, OperationID: id, EncryptedRequest: "encrypted-service", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateServiceResource(ctx, run, resource); err != nil {
		t.Fatal(err)
	}
	resource, err := data.ClaimServiceResource(ctx, id, 1, id, "provisioning", "cache-source", now, "")
	if err != nil {
		t.Fatal(err)
	}
	resource.State, resource.ResourceID = "ready", id+"-container"
	run.State, run.ServiceID = "succeeded", resource.ServiceID
	service := core.Service{ID: resource.ServiceID, Name: id, ProjectID: project, ProvisionRunID: id, Revision: 1}
	if err = data.SaveServiceResource(ctx, resource, run, &service); err != nil {
		t.Fatal(err)
	}
	return core.ServiceProvisionRequest{Run: run, ServiceType: "postgresql", Password: "cache-database-password"}
}

func queuedCacheRecovery(t *testing.T, target, action string) (*API, core.PrivateNetwork, edge.Session, remoteruntime.Request, string) {
	t.Helper()
	a, node, session, app := runtimeAPIFixture(t)
	ctx, now := context.Background(), time.Now().UTC()
	server, err := a.store.GetServer(ctx, app.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	source := ownedCacheRecoverySource(t, a, app.ProjectID, server, "cache-source")
	b := core.WorkloadBackup{ID: "cache-backup", ArtifactID: "cache-backup", ProjectID: app.ProjectID, ServerID: server.ID, NodeID: node.ID, SourceRunID: source.Run.ID, SourceResourceID: "cache-source-container", StorageID: "cache-volume", State: "creating", Revision: 1, Policy: "retain", Location: "target-local", Consistency: "database-native", Format: "postgresql-custom", CreatedAt: now, UpdatedAt: now}
	input := core.WorkloadBackupRequest{OperationID: b.ID, Action: "backup", Backup: b, Source: source, Storage: core.StorageResource{ID: b.StorageID, ProjectID: app.ProjectID, ServerID: server.ID}, Key: strings.Repeat("a", 64)}
	b.EncryptedInput, err = a.encryptWorkloadBackup(b.ID, "accepted", input)
	if err != nil {
		t.Fatal(err)
	}
	capture := newBackupOperation(b.ID, b, "backup")
	capture.EncryptedInput, err = a.encryptWorkloadBackup(capture.ID, "operation", input)
	if err != nil {
		t.Fatal(err)
	}
	backups := a.store.(store.WorkloadBackupStore)
	if err = backups.CreateWorkloadBackup(ctx, b, capture); err != nil {
		t.Fatal(err)
	}
	if err = a.store.CreateSecret(ctx, core.Secret{ID: "cache-signing", Name: "Cache signing", Type: core.SecretTypeJSON, EncryptedValue: "encrypted-signing", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	destination := core.BackupObjectStore{ID: "cache-store", ProjectID: app.ProjectID, Name: "Cache store", CredentialSecretID: "cache-signing", Config: backupstore.Config{Endpoint: "https://objects.example.invalid", Bucket: "backups", Region: "us-east-1", Prefix: "dispatch", MaxBytes: 1 << 20}, CreatedAt: now}
	if err = a.store.(store.BackupObjectStoreStore).CreateBackupObjectStore(ctx, destination); err != nil {
		t.Fatal(err)
	}
	// Legacy archives omit all newly added offsite verification and export fields.
	b.State, b.VerificationState, b.CleanupState, b.Checksum = "ready", "verified", "complete", strings.Repeat("b", 64)
	b.Offsite = &core.BackupOffsiteArtifact{StoreID: destination.ID, ArchiveKey: "archive", ManifestKey: "manifest", ManifestChecksum: strings.Repeat("c", 64), ImageReference: "postgres@sha256:" + strings.Repeat("d", 64), ConfirmedAt: now}
	if target == "retired-source" {
		b.LocalState = "retired"
	}
	capture.State, capture.CleanupState = "succeeded", "complete"
	if err = backups.CompleteWorkloadBackupOperation(ctx, b, capture); err != nil {
		t.Fatal(err)
	}
	b, err = backups.GetWorkloadBackup(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	execution := server
	recovery := source
	if target == "other-target" {
		if err = json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/private-networks", map[string]string{"name": "cache-recovery", "driver": "dispatch_agent"}, 201), &node); err != nil {
			t.Fatal(err)
		}
		session = enrollNodeTest(t, a, node)
		execution = core.Server{ID: "cache-target", Name: "Cache target", AgentNodeID: node.ID, Address: "agent:" + node.ID, Runtime: "docker", CreatedAt: now}
		if err = a.store.CreateServer(ctx, execution); err != nil {
			t.Fatal(err)
		}
		recovery = ownedCacheRecoverySource(t, a, app.ProjectID, execution, "cache-destination")
	}
	effective := strings.TrimPrefix(action, "reconcile-")
	op := newBackupOperation("cache-recovery-operation", b, effective)
	op.OffsiteStoreID = destination.ID
	op.ExecutionServerID, op.ExecutionNodeID = execution.ID, node.ID
	credential, err := a.store.(store.RuntimeJobStore).GetEdgeCredential(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	op.ExecutionGeneration = credential.Generation
	input.OperationID, input.Action, input.Backup = op.ID, effective, b
	input.ExecutionNodeGeneration = credential.Generation
	input.OffsiteAccess = &backupstore.Access{StoreID: destination.ID, MaxBytes: 1 << 20, Archive: backupstore.ObjectAccess{Key: "archive", Get: "https://objects.example.invalid/archive"}, Manifest: backupstore.ObjectAccess{Key: "manifest", Get: "https://objects.example.invalid/manifest"}}
	if target == "other-target" || effective == "restore" {
		input.Destination, input.ExpectedDestination = &recovery, recovery.Run.ID+"-container"
		input.DestinationStorage = &core.StorageResource{ProjectID: app.ProjectID, ServerID: execution.ID}
		op.TargetRunID, op.TargetResourceID = recovery.Run.ID, input.ExpectedDestination
	}
	op.EncryptedInput, err = a.encryptWorkloadBackup(op.ID, "operation", input)
	if err != nil {
		t.Fatal(err)
	}
	if err = backups.CreateWorkloadBackupOperation(ctx, op, b.Revision); err != nil {
		t.Fatal(err)
	}
	if action != effective {
		input.Action, input.RecoveryAction = "reconcile", effective
	}
	request := remoteruntime.NewWorkloadBackupRequest(input, execution)
	jobID := "cache-runtime-job"
	if _, err = a.runtimeBroker().Submit(ctx, jobID, request); err != nil {
		t.Fatal(err)
	}
	return a, node, session, request, jobID
}

func TestBackupCacheCleanupCapabilityBeforePayloadRelease(t *testing.T) {
	for _, target := range []string{"original-source", "other-target", "retired-source"} {
		for _, action := range []string{"verify", "restore", "reconcile-verify", "reconcile-restore"} {
			for _, modern := range []bool{false, true} {
				label := "legacy"
				if modern {
					label = "modern"
				}
				t.Run(target+"-"+action+"-"+label, func(t *testing.T) {
					a, node, session, request, id := queuedCacheRecovery(t, target, action)
					caps, want := legacyOffsiteCapabilities, 204
					if modern {
						caps = cacheCleanupCapabilities
					}
					if modern || target == "original-source" {
						want = 200
					}
					raw := cacheRuntimeRequest(t, a, node, session.Token, "next", "GET", caps, nil, want)
					job, err := a.runtimeBroker().Store.GetRuntimeJob(context.Background(), id)
					if err != nil {
						t.Fatal(err)
					}
					if want == 204 {
						if len(raw) != 0 || job.State != "failed" || job.EncryptedRequest != "" {
							t.Fatal("unsupported engine received recovery inputs", job.State)
						}
						result, err := a.runtimeBroker().Wait(context.Background(), id, nil)
						if err == nil || result.Code != runtimecontract.Unsupported || result.WorkloadBackup == nil || result.WorkloadBackup.CleanupState != "complete" {
							t.Fatal("undispatched job lost its settled failure", err)
						}
						cacheRuntimeRequest(t, a, node, session.Token, "next", "GET", caps, nil, 204)
					} else {
						var lease remoteruntime.LeasedJob
						if err = json.Unmarshal(raw, &lease); err != nil || lease.ID != id || lease.Request.Operation != request.Operation {
							t.Fatal("supported engine did not receive the original typed recovery", err)
						}
						cacheRuntimeRequest(t, a, node, session.Token, id+"/heartbeat", "POST", caps, remoteruntime.Heartbeat{LeaseToken: lease.LeaseToken}, 200)
					}
				})
			}
		}
	}
}

func TestBackupCacheCleanupCapabilityLossFencesLeaseAndKeepsRecoveryInputs(t *testing.T) {
	for _, target := range []string{"other-target", "retired-source"} {
		for _, action := range []string{"verify", "restore", "reconcile-verify", "reconcile-restore"} {
			t.Run(target+"-"+action, func(t *testing.T) {
				a, node, session, request, id := queuedCacheRecovery(t, target, action)
				var lease remoteruntime.LeasedJob
				if err := json.Unmarshal(cacheRuntimeRequest(t, a, node, session.Token, "next", "GET", cacheCleanupCapabilities, nil, 200), &lease); err != nil {
					t.Fatal(err)
				}
				cacheRuntimeRequest(t, a, node, session.Token, id+"/heartbeat", "POST", legacyOffsiteCapabilities, remoteruntime.Heartbeat{LeaseToken: "stale-lease"}, 409)
				job, err := a.runtimeBroker().Store.GetRuntimeJob(context.Background(), id)
				if err != nil || job.CancelRequested || job.State != "running" {
					t.Fatal("stale heartbeat changed current execution", err)
				}
				cacheRuntimeRequest(t, a, node, session.Token, id+"/heartbeat", "POST", legacyOffsiteCapabilities, remoteruntime.Heartbeat{LeaseToken: lease.LeaseToken}, 422)
				job, err = a.runtimeBroker().Store.GetRuntimeJob(context.Background(), id)
				if err != nil || job.State != "unknown" || !job.CancelRequested || job.LeaseToken != "" || job.EncryptedRequest != "" {
					t.Fatal("unsupported cleanup engine kept a usable lease", job.State, err)
				}
				op, err := a.store.(store.WorkloadBackupStore).GetWorkloadBackupOperation(context.Background(), request.WorkloadBackup.OperationID)
				if err != nil || op.EncryptedInput == "" {
					t.Fatal("capability loss erased original recovery input", err)
				}
				evidence := core.WorkloadBackupResult{BackupID: request.WorkloadBackup.Backup.ID, ProjectID: request.Application.ProjectID, OperationID: request.WorkloadBackup.OperationID, ArtifactID: request.WorkloadBackup.Backup.ID, State: map[string]string{"verify": "verified", "restore": "restored"}[strings.TrimPrefix(action, "reconcile-")], CleanupState: "complete"}
				cacheRuntimeRequest(t, a, node, session.Token, id+"/complete", "POST", legacyOffsiteCapabilities, remoteruntime.Completion{LeaseToken: lease.LeaseToken, Result: remoteruntime.Result{State: "succeeded", WorkloadBackup: &evidence}}, 422)
				job, _ = a.runtimeBroker().Store.GetRuntimeJob(context.Background(), id)
				if job.State != "unknown" || job.EncryptedResult != "" {
					t.Fatal("unsupported result claimed confirmed cleanup", job.State)
				}
			})
		}
	}
}
