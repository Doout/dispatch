package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/secretvalue"
	"github.com/doout/dispatch/internal/store"
)

func offsiteIdentity(t *testing.T, a *API, project string) (string, core.PrincipalGrant) {
	t.Helper()
	var account core.ServiceAccount
	json.Unmarshal(automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts", map[string]any{"name": "offsite-recovery"}, 201).Body.Bytes(), &account)
	var issued struct {
		Token string `json:"token"`
	}
	json.Unmarshal(automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts/"+account.ID+"/credentials", map[string]any{"name": "recovery", "expiresAt": time.Now().Add(time.Hour)}, 201).Body.Bytes(), &issued)
	grant := core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: project, Permissions: []core.Permission{core.PermissionProjectView, core.PermissionProjectConfigure, core.PermissionDeploymentRun}}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	return issued.Token, grant
}
func offsiteRegistration(t *testing.T, a *API, project, name string) map[string]any {
	t.Helper()
	ctx := context.Background()
	const credential = `{"accessKeyId":"FIXTUREACCESS","secretAccessKey":"fixture-signing-secret","sessionToken":"fixture-session-secret"}`
	encrypted, err := a.eventConfig.Vault.Encrypt("secret:offsite-credential", []byte(credential))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.GetSecret(ctx, "offsite-credential"); errors.Is(err, store.ErrNotFound) {
		if err = a.store.CreateSecret(ctx, core.Secret{ID: "offsite-credential", Name: "Scoped backup signing", Type: core.SecretTypeJSON, EncryptedValue: encrypted, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	a.secretResolver = secretvalue.New(a.store, a.eventConfig.Vault)
	return map[string]any{"projectId": project, "name": name, "credentialSecretId": "offsite-credential", "config": backupstore.Config{Endpoint: "https://objects.example.invalid", Bucket: "recovery", Region: "us-east-1", Prefix: "dispatch", MaxBytes: 1 << 20}}
}
func assertOffsitePublic(t *testing.T, raw string) {
	t.Helper()
	for _, private := range []string{"fixture-signing-secret", "fixture-session-secret", "FIXTUREACCESS", "X-Amz-Signature", "X-Amz-Credential", "encryptedInput"} {
		if strings.Contains(raw, private) {
			t.Fatalf("offsite response exposed private material %s", private)
		}
	}
}

func TestOffsiteBackupRegistrationDiscoveryAndScopedExport(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	token, grant := offsiteIdentity(t, a, source.ProjectID)
	input := offsiteRegistration(t, a, source.ProjectID, "project-recovery")
	automationRequest(t, a, token, "POST", "/api/v1/workload-backup-stores", input, 403)
	var destination core.BackupObjectStore
	raw := automationRequest(t, a, "secret", "POST", "/api/v1/workload-backup-stores", input, 201).Body.String()
	assertOffsitePublic(t, raw)
	if json.Unmarshal([]byte(raw), &destination) != nil || destination.CredentialSecretID != "offsite-credential" {
		t.Fatal("lost safe registration metadata")
	}
	foreign := core.Project{ID: "offsite-other-project", Name: "Other", CreatedAt: time.Now().UTC()}
	if err := a.store.CreateProject(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	var other core.BackupObjectStore
	raw = automationRequest(t, a, "secret", "POST", "/api/v1/workload-backup-stores", offsiteRegistration(t, a, foreign.ID, "foreign-recovery"), 201).Body.String()
	json.Unmarshal([]byte(raw), &other)
	listed := automationRequest(t, a, token, "GET", "/api/v1/workload-backup-stores", nil, 200)
	assertOffsitePublic(t, listed.Body.String())
	var stores []core.BackupObjectStore
	if json.Unmarshal(listed.Body.Bytes(), &stores) != nil || len(stores) != 1 || stores[0].ID != destination.ID {
		t.Fatal("discovery exposed foreign store", listed.Body.String())
	}
	assertOffsitePublic(t, automationRequest(t, a, token, "GET", "/api/v1/workload-backup-stores/"+destination.ID, nil, 200).Body.String())
	automationRequest(t, a, token, "GET", "/api/v1/workload-backup-stores/"+other.ID, nil, 403)
	automationRequest(t, a, token, "GET", "/api/v1/workload-backup-stores?projectId="+foreign.ID, nil, 403)
	var mu sync.Mutex
	exports := 0
	a.workloadBackupBackend = func(_ context.Context, r core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
		result := core.WorkloadBackupResult{BackupID: r.Backup.ID, ProjectID: r.Backup.ProjectID, OperationID: r.OperationID, ArtifactID: r.Backup.ID, State: "ready", Checksum: strings.Repeat("a", 64), PlaintextChecksum: strings.Repeat("b", 64), ImageID: "sha256:" + strings.Repeat("c", 64), Bytes: 42, CleanupState: "complete"}
		if r.Action == "export" {
			if r.OffsiteAccess == nil || r.OffsiteAccess.StoreID != destination.ID || r.OffsiteAccess.Archive.Put == "" {
				return result, errors.New("missing explicit write access")
			}
			mu.Lock()
			exports++
			mu.Unlock()
			result.State = "exported"
			result.Offsite = &core.BackupOffsiteArtifact{StoreID: destination.ID, ArchiveKey: r.OffsiteAccess.Archive.Key, ManifestKey: r.OffsiteAccess.Manifest.Key, ManifestChecksum: strings.Repeat("d", 64), ImageReference: "postgres:17", ConfirmedAt: time.Now().UTC()}
		}
		return result, nil
	}
	capture := mutationRequest(a, "secret", "POST", "/api/v1/workload-backups", "offsite-capture", map[string]any{"sourceRunId": source.RunID})
	if capture.Code != 202 {
		t.Fatal(capture.Code, capture.Body.String())
	}
	receipt := decodeMutation(t, capture)
	awaitBackupOperation(t, a, receipt.OperationID, "succeeded")
	path := "/api/v1/workload-backups/" + receipt.OperationID + "/export"
	for _, permissions := range [][]core.Permission{{core.PermissionProjectView, core.PermissionDeploymentRun}, {core.PermissionProjectView, core.PermissionProjectConfigure}} {
		grant.Permissions = permissions
		automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
		if denied := mutationRequest(a, token, "POST", path, "offsite-export", map[string]any{"storeId": destination.ID}); denied.Code != 403 {
			t.Fatal("incomplete grant accepted export", denied.Code, denied.Body.String())
		}
	}
	grant.Permissions = []core.Permission{core.PermissionProjectView, core.PermissionProjectConfigure, core.PermissionDeploymentRun}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	if denied := mutationRequest(a, token, "POST", path, "offsite-foreign-export", map[string]any{"storeId": other.ID}); denied.Code != 409 {
		t.Fatal("accepted foreign store", denied.Code, denied.Body.String())
	}
	exported := mutationRequest(a, token, "POST", path, "offsite-export", map[string]any{"storeId": destination.ID})
	if exported.Code != 202 {
		t.Fatal(exported.Code, exported.Body.String())
	}
	exportReceipt := decodeMutation(t, exported)
	awaitBackupOperation(t, a, exportReceipt.OperationID, "succeeded")
	replay := mutationRequest(a, token, "POST", path, "offsite-export", map[string]any{"storeId": destination.ID})
	if replay.Code != 202 || decodeMutation(t, replay).ID != exportReceipt.ID {
		t.Fatal("export did not reuse receipt", replay.Body.String())
	}
	raw = automationRequest(t, a, token, "GET", "/api/v1/workload-backups/"+receipt.OperationID, nil, 200).Body.String()
	assertOffsitePublic(t, raw)
	assertOffsitePublic(t, automationRequest(t, a, token, "GET", "/api/v1/workload-backups/"+receipt.OperationID+"/operations", nil, 200).Body.String())
	mu.Lock()
	count := exports
	mu.Unlock()
	if count != 1 {
		t.Fatal("export replay repeated side effect", count)
	}
	grant.Permissions = []core.Permission{core.PermissionProjectView}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	if denied := mutationRequest(a, token, "POST", path, "offsite-export", map[string]any{"storeId": destination.ID}); denied.Code != 403 {
		t.Fatal("replay ignored revoked grants", denied.Code)
	}
}

func TestOffsiteBackupVerificationUsesFreshDestinationAfterSourceLoss(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	var destinationStore core.BackupObjectStore
	json.Unmarshal(automationRequest(t, a, "secret", "POST", "/api/v1/workload-backup-stores", offsiteRegistration(t, a, source.ProjectID, "disaster-recovery"), 201).Body.Bytes(), &destinationStore)
	var mu sync.Mutex
	verified, restored := 0, 0
	freshID := "offsite-fresh-target"
	a.workloadBackupBackend = func(_ context.Context, r core.WorkloadBackupRequest, s core.Server) (core.WorkloadBackupResult, error) {
		result := core.WorkloadBackupResult{BackupID: r.Backup.ID, ProjectID: r.Backup.ProjectID, OperationID: r.OperationID, ArtifactID: r.Backup.ID, State: "ready", Checksum: strings.Repeat("a", 64), PlaintextChecksum: strings.Repeat("b", 64), ImageID: "sha256:" + strings.Repeat("c", 64), Bytes: 42, CleanupState: "complete"}
		switch r.Action {
		case "export":
			result.State = "exported"
			result.Offsite = &core.BackupOffsiteArtifact{StoreID: destinationStore.ID, ArchiveKey: r.OffsiteAccess.Archive.Key, ManifestKey: r.OffsiteAccess.Manifest.Key, ManifestChecksum: strings.Repeat("d", 64), ImageReference: "postgres:17", ConfirmedAt: time.Now().UTC()}
		case "verify":
			if s.ID != freshID || r.Destination == nil || r.OffsiteAccess == nil || r.OffsiteAccess.Archive.Put != "" || r.OffsiteAccess.Archive.Get == "" {
				return result, errors.New("recovery touched lost source or lacked read-only offsite access")
			}
			mu.Lock()
			verified++
			mu.Unlock()
			result.State = "verified"
		case "restore":
			if s.ID != freshID || r.Destination == nil || r.OffsiteAccess == nil || r.OffsiteAccess.Archive.Put != "" {
				return result, errors.New("reviewed restore did not use the frozen fresh target")
			}
			mu.Lock()
			restored++
			mu.Unlock()
			result.State = "restored"
		}
		return result, nil
	}
	capture := mutationRequest(a, "secret", "POST", "/api/v1/workload-backups", "source-loss-capture", map[string]any{"sourceRunId": source.RunID, "checks": []map[string]string{{"query": "SELECT 1", "expected": "1"}}})
	if capture.Code != 202 {
		t.Fatal(capture.Code, capture.Body.String())
	}
	receipt := decodeMutation(t, capture)
	awaitBackupOperation(t, a, receipt.OperationID, "succeeded")
	base := "/api/v1/workload-backups/" + receipt.OperationID
	exported := mutationRequest(a, "secret", "POST", base+"/export", "source-loss-export", map[string]any{"storeId": destinationStore.ID})
	if exported.Code != 202 {
		t.Fatal(exported.Code, exported.Body.String())
	}
	awaitBackupOperation(t, a, decodeMutation(t, exported).OperationID, "succeeded")
	fresh := core.Server{ID: freshID, Name: "Fresh recovery target", Runtime: core.ServerRuntimeDocker, State: "ready", Address: "local", CreatedAt: time.Now().UTC()}
	if err := a.store.CreateServer(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	doc := fmt.Sprintf("apiVersion: dispatch/v1alpha1\nkind: ServiceTemplate\nmetadata: {name: recovery-fresh}\nspec:\n  serviceType: postgresql\n  provision:\n    docker: {serverRef: %s}\n", fresh.ID)
	var template core.SavedServiceTemplate
	json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/service-templates", map[string]any{"projectId": source.ProjectID, "document": doc}, 201), &template)
	var run core.ServiceProvisionRun
	json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/service-templates/"+template.ID+"/runs", map[string]any{"name": "fresh-recovery-db"}, 202), &run)
	destination := awaitServiceResource(t, a, run.ID, "ready")
	a.deploy.Storage.Backend = &storageTestBackend{observation: core.StorageObservation{Resource: core.StorageResource{Kind: "docker_volume", Name: deploy.ServiceResourceName(run.ID) + "-data", Identity: "fresh-owned-volume", Evidence: "labels"}, Labels: map[string]string{"dispatch.managed-by": "dispatch", "dispatch.project": source.ProjectID, "dispatch.service-template": template.ID, "dispatch.service-provision": run.ID}}}
	lost, err := a.store.GetServer(ctx, source.Target.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	lost.State = "offline"
	lost.AgentNodeID = "lost-source-node"
	lost.Address = "unavailable-source.invalid"
	if err = a.store.UpdateServer(ctx, lost); err != nil {
		t.Fatal(err)
	}
	accepted := mutationRequest(a, "secret", "POST", base+"/verify", "fresh-target-verify", map[string]any{"destinationRunId": destination.RunID})
	if accepted.Code != 202 {
		t.Fatal(accepted.Code, accepted.Body.String())
	}
	operation := awaitBackupOperation(t, a, decodeMutation(t, accepted).OperationID, "succeeded")
	if operation.ExecutionServerID != fresh.ID || operation.TargetRunID != destination.RunID {
		t.Fatal("lost frozen fresh destination", operation)
	}
	replay := mutationRequest(a, "secret", "POST", base+"/verify", "fresh-target-verify", map[string]any{"destinationRunId": destination.RunID})
	if replay.Code != 202 || decodeMutation(t, replay).OperationID != operation.ID {
		t.Fatal("verification replay changed destination", replay.Body.String())
	}
	mu.Lock()
	count := verified
	mu.Unlock()
	if count != 1 {
		t.Fatal("verification repeated", count)
	}
	b, err := a.store.(store.WorkloadBackupStore).GetWorkloadBackup(ctx, receipt.OperationID)
	if err != nil || b.VerificationState != "verified" || b.CleanupState != "complete" {
		t.Fatal("missing isolated verification evidence", b, err)
	}
	retained, err := a.store.(store.ServiceResourceStore).GetServiceResource(ctx, destination.RunID)
	if err != nil || retained.State != "ready" || retained.ResourceID != destination.ResourceID {
		t.Fatal("verification altered live destination", retained, err)
	}
	restorePath := base + "/restore/" + destination.RunID
	serviceRequestTest(t, a, "POST", restorePath, map[string]any{}, 422)
	var review destructiveReview
	json.Unmarshal(serviceRequestTest(t, a, "POST", restorePath+"/preview", nil, 200), &review)
	if review.BlockedReason != "" || review.Name != destination.Name {
		t.Fatal("fresh destination restore review unavailable", review)
	}
	confirmation := map[string]any{"confirmation": destructiveConfirmation{ResourceID: receipt.OperationID, Action: "restore", ExpectedVersion: review.Version, ConfirmName: destination.Name}}
	restore := mutationRequest(a, "secret", "POST", restorePath, "fresh-target-reviewed-restore", confirmation)
	if restore.Code != 202 {
		t.Fatal(restore.Code, restore.Body.String())
	}
	restoredOperation := awaitBackupOperation(t, a, decodeMutation(t, restore).OperationID, "succeeded")
	if restoredOperation.TargetRunID != destination.RunID || restoredOperation.ExecutionServerID != fresh.ID {
		t.Fatal("reviewed restore changed target", restoredOperation)
	}
	mu.Lock()
	restores := restored
	mu.Unlock()
	if restores != 1 {
		t.Fatal("reviewed restore was not executed once", restores)
	}
}
