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

func TestReviewedBackupRetirementAPIReceiptsScopeAndOffsiteRecovery(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	token, grant := offsiteIdentity(t, a, source.ProjectID)
	var destination core.BackupObjectStore
	json.Unmarshal(automationRequest(t, a, "secret", "POST", "/api/v1/workload-backup-stores", offsiteRegistration(t, a, source.ProjectID, "Retirement store"), 201).Body.Bytes(), &destination)
	capability := map[string]any{"enabled": true, "expectedEnabled": false, "confirmName": destination.Name}
	automationRequest(t, a, token, "PUT", "/api/v1/workload-backup-stores/"+destination.ID+"/conditional-delete", capability, 403)
	automationRequest(t, a, "secret", "PUT", "/api/v1/workload-backup-stores/"+destination.ID+"/conditional-delete", capability, 200)
	automationRequest(t, a, "secret", "PUT", "/api/v1/workload-backup-stores/"+destination.ID+"/conditional-delete", capability, 409)
	var mu sync.Mutex
	counts := map[string]int{}
	lostReply := true
	a.workloadBackupBackend = func(_ context.Context, input core.WorkloadBackupRequest, server core.Server) (core.WorkloadBackupResult, error) {
		mu.Lock()
		defer mu.Unlock()
		counts[input.Action]++
		result := core.WorkloadBackupResult{BackupID: input.Backup.ID, ProjectID: input.Backup.ProjectID, OperationID: input.OperationID, ArtifactID: input.Backup.ID, Checksum: strings.Repeat("a", 64), PlaintextChecksum: strings.Repeat("b", 64), Bytes: 42, ImageID: "sha256:" + strings.Repeat("c", 64), CleanupState: "complete"}
		switch input.Action {
		case "backup":
			result.State = "ready"
		case "export":
			now := time.Now().UTC()
			result.State = "exported"
			result.Offsite = &core.BackupOffsiteArtifact{StoreID: destination.ID, ArchiveKey: input.OffsiteAccess.Archive.Key, ManifestKey: input.OffsiteAccess.Manifest.Key, ManifestChecksum: strings.Repeat("d", 64), ImageReference: "docker.io/library/postgres@sha256:" + strings.Repeat("e", 64), ConfirmedAt: now, VerifiedAt: &now, VerificationState: "verified"}
		case "verify":
			result.State = "verified"
		case "retire-local":
			if input.OffsiteAccess == nil || input.OffsiteAccess.Archive.Delete != "" {
				return result, errors.New("local retirement was granted object deletion")
			}
			result.State = "local-retired"
		case "delete-offsite":
			if server.ID != "" || input.OffsiteAccess == nil || input.OffsiteAccess.Archive.Delete == "" || input.OffsiteAccess.Archive.Put != "" {
				return result, errors.New("offsite deletion authority changed")
			}
			if lostReply {
				lostReply = false
				result.State, result.CleanupState = "unknown", "pending"
				return result, errors.New("lost object reply")
			}
			result.State = "offsite-deleted"
		default:
			return result, errors.New("unexpected mutation")
		}
		return result, nil
	}
	capture := func(key string) string {
		t.Helper()
		r := mutationRequest(a, token, "POST", "/api/v1/workload-backups", key, map[string]any{"sourceRunId": source.RunID})
		if r.Code != 202 {
			t.Fatal(r.Code, r.Body.String())
		}
		id := decodeMutation(t, r).OperationID
		awaitBackupOperation(t, a, id, "succeeded")
		return id
	}
	id := capture("retirement-first-capture")
	base := "/api/v1/workload-backups/" + id
	exported := mutationRequest(a, token, "POST", base+"/export", "retirement-export", map[string]any{"storeId": destination.ID})
	if exported.Code != 202 {
		t.Fatal(exported.Code, exported.Body.String())
	}
	awaitBackupOperation(t, a, decodeMutation(t, exported).OperationID, "succeeded")
	var review destructiveReview
	json.Unmarshal(automationRequest(t, a, token, "POST", base+"/retire-local-preview", nil, 200).Body.Bytes(), &review)
	if review.BlockedReason == "" {
		t.Fatal("upload confirmation allowed retirement before independent verification")
	}
	verification := mutationRequest(a, token, "POST", base+"/verify", "retirement-offsite-verify", nil)
	if verification.Code != 202 {
		t.Fatal(verification.Code, verification.Body.String())
	}
	awaitBackupOperation(t, a, decodeMutation(t, verification).OperationID, "succeeded")
	review = destructiveReview{}
	json.Unmarshal(automationRequest(t, a, token, "POST", base+"/retire-local-preview", nil, 200).Body.Bytes(), &review)
	if review.BlockedReason != "" || len(review.Resources) < 5 {
		t.Fatal("exact offsite preservation identity absent", review)
	}
	confirm := map[string]any{"confirmation": destructiveConfirmation{ResourceID: id, Action: "retire-local", ExpectedVersion: review.Version, ConfirmName: id}}
	automationRequest(t, a, token, "POST", base+"/retire-local", confirm, 422)
	wrong := map[string]any{"confirmation": destructiveConfirmation{ResourceID: id, Action: "delete-offsite", ExpectedVersion: review.Version, ConfirmName: id}}
	bad := mutationRequest(a, token, "POST", base+"/retire-local", "retirement-wrong-action", wrong)
	if bad.Code != 422 {
		t.Fatal(bad.Code, bad.Body.String())
	}
	grant.Permissions = []core.Permission{core.PermissionProjectView, core.PermissionProjectConfigure}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	denied := mutationRequest(a, token, "POST", base+"/retire-local", "retirement-no-run", confirm)
	if denied.Code != 403 {
		t.Fatal("missing deployment.run granted retirement", denied.Code)
	}
	grant.Permissions = append(grant.Permissions, core.PermissionDeploymentRun)
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	retired := mutationRequest(a, token, "POST", base+"/retire-local", "retirement-original", confirm)
	if retired.Code != 202 {
		t.Fatal(retired.Code, retired.Body.String())
	}
	receipt := decodeMutation(t, retired)
	awaitBackupOperation(t, a, receipt.OperationID, "succeeded")
	replay := mutationRequest(a, token, "POST", base+"/retire-local", "retirement-original", confirm)
	if replay.Code != 202 || decodeMutation(t, replay).OperationID != receipt.OperationID {
		t.Fatal("retirement retry lost original receipt", replay.Code, replay.Body.String())
	}
	backups := a.store.(store.WorkloadBackupStore)
	b, err := backups.GetWorkloadBackup(ctx, id)
	if err != nil || b.LocalState != "retired" || b.EncryptedInput == "" || b.State != "ready" {
		t.Fatal("retirement lost recovery metadata", b, err)
	}
	var targetReview destructiveReview
	json.Unmarshal(automationRequest(t, a, "secret", "POST", "/api/v1/servers/"+b.ServerID+"/delete-preview", nil, 200).Body.Bytes(), &targetReview)
	if strings.Contains(targetReview.BlockedReason, "Retained workload backups") {
		t.Fatal("confirmed retirement did not release backup target protection")
	}
	review = destructiveReview{}
	json.Unmarshal(automationRequest(t, a, token, "POST", base+"/delete-offsite-preview", nil, 200).Body.Bytes(), &review)
	if review.BlockedReason == "" {
		t.Fatal("last offsite copy can be removed")
	}
	alternative := capture("retirement-alternative-capture")
	verified := mutationRequest(a, token, "POST", "/api/v1/workload-backups/"+alternative+"/verify", "retirement-alternative-verify", nil)
	if verified.Code != 202 {
		t.Fatal(verified.Code, verified.Body.String())
	}
	awaitBackupOperation(t, a, decodeMutation(t, verified).OperationID, "succeeded")
	review = destructiveReview{}
	json.Unmarshal(automationRequest(t, a, token, "POST", base+"/delete-offsite-preview", nil, 200).Body.Bytes(), &review)
	if review.BlockedReason != "" {
		t.Fatal("verified alternative was ignored", review.BlockedReason)
	}
	confirm = map[string]any{"confirmation": destructiveConfirmation{ResourceID: id, Action: "delete-offsite", ExpectedVersion: review.Version, ConfirmName: id}}
	deleted := mutationRequest(a, token, "POST", base+"/delete-offsite", "offsite-delete-original", confirm)
	if deleted.Code != 202 {
		t.Fatal(deleted.Code, deleted.Body.String())
	}
	deleteReceipt := decodeMutation(t, deleted)
	awaitBackupOperation(t, a, deleteReceipt.OperationID, "unknown")
	b, err = backups.GetWorkloadBackup(ctx, id)
	if err != nil || b.Offsite.DeletedAt != nil || b.State != "ready" {
		t.Fatal("uncertain cleanup falsely removed recovery identity", b, err)
	}
	blocked := automationRequest(t, a, token, "POST", base+"/delete-offsite-preview", nil, 200)
	json.Unmarshal(blocked.Body.Bytes(), &review)
	if review.BlockedReason == "" {
		t.Fatal("uncertain operation released references")
	}
	recovered, err := backups.ClaimWorkloadBackupRecovery(ctx, deleteReceipt.OperationID, time.Now().Add(32*time.Minute), "restarted-controller")
	if err != nil {
		t.Fatal(err)
	}
	a.executeWorkloadBackupOperation(recovered, true)
	awaitBackupOperation(t, a, recovered.ID, "succeeded")
	b, err = backups.GetWorkloadBackup(ctx, id)
	if err != nil || b.State != "deleted" || b.Offsite.DeletedAt == nil || b.EncryptedInput == "" {
		t.Fatal("reconciled deletion lost tombstone or recovery evidence", b, err)
	}
	replay = mutationRequest(a, token, "POST", base+"/delete-offsite", "offsite-delete-original", confirm)
	if replay.Code != 202 || decodeMutation(t, replay).OperationID != deleteReceipt.OperationID {
		t.Fatal("deletion retry repeated logical operation")
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["retire-local"] != 1 || counts["delete-offsite"] != 2 {
		t.Fatal("logical actions repeated outside reconciliation", counts)
	}
	assertOffsitePublic(t, automationRequest(t, a, token, "GET", base, nil, 200).Body.String())
}
