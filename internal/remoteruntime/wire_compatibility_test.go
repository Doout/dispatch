package remoteruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const backupRecoveryWireDigest = "9fd2aa02e5a6991518a95c0e63252f18b3f27a333206a27f2939a106b96cdaa3"

func readWireFixture(t *testing.T, name, digest string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "wire", name))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digest {
		t.Fatalf("%s changed its frozen wire digest; review compatibility before updating the fixture", name)
	}
	return raw
}

func checkWireEncoding(t *testing.T, raw []byte, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, raw) {
		t.Fatalf("saved wire bytes changed after decoding or reconstruction\nwant %s\ngot  %s", raw, encoded)
	}
}

func TestSavedRuntimeRequestsKeepWireBytesAndDigest(t *testing.T) {
	for _, tc := range []struct {
		name, digest string
	}{
		{"deploy-request.json", "f855eb9a99d4ab49710fb604b4880176059aff7a6167fe3f312b0b21600297fe"},
		{"backup-recovery-request.json", backupRecoveryWireDigest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := readWireFixture(t, tc.name, tc.digest)
			var saved Request
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			if err := saved.Validate(); err != nil {
				t.Fatalf("saved request no longer validates: %v", err)
			}
			checkWireEncoding(t, raw, saved)
			var reconstructed Request
			if saved.WorkloadBackup != nil {
				reconstructed = NewWorkloadBackupRequest(*saved.WorkloadBackup, saved.Server)
				if saved.WorkloadBackup.OperationID != "fixture-original-restore" || saved.WorkloadBackup.Action != "reconcile" || saved.WorkloadBackup.RecoveryAction != "restore" || saved.WorkloadBackup.ExecutionNodeGeneration != 7 || saved.BackupCacheCleanupCapability() != WorkloadBackupRetireInspect {
					t.Fatal("saved recovery lost its original operation, action or cleanup requirement")
				}
			} else {
				deployment := saved.Deployment
				deployment.Snapshot = saved.Snapshot
				reconstructed = NewRequest(saved.Operation, deployment, saved.App(), saved.Server)
			}
			if err := reconstructed.Validate(); err != nil {
				t.Fatalf("reconstructed request no longer validates: %v", err)
			}
			checkWireEncoding(t, raw, reconstructed)
		})
	}
}

func TestRuntimeRecoveryEnvelopesKeepOriginalLeaseAndOperation(t *testing.T) {
	var leased LeasedJob
	raw := readWireFixture(t, "leased-backup-recovery.json", "7723d6bc2f21c1c5d0adbb3438f50c3b39e8522d62f75c8fdcf6eb97f5531cea")
	if err := json.Unmarshal(raw, &leased); err != nil {
		t.Fatal(err)
	}
	if err := leased.Request.Validate(); err != nil {
		t.Fatal(err)
	}
	if leased.ID != "fixture-recovery-job" || leased.Attempt != 2 || !leased.CancelRequested || leased.LeaseToken != "fixture-lease-token" || leased.Request.WorkloadBackup.OperationID == leased.ID || leased.Digest != backupRecoveryWireDigest || leased.ExpiresAt.Format("2006-01-02T15:04:05Z07:00") != "2025-01-02T03:34:05Z" {
		t.Fatal("saved recovery lease lost its original identity, attempt, deadline or digest")
	}
	checkWireEncoding(t, raw, leased)
	checkWireEncoding(t, readWireFixture(t, "backup-recovery-request.json", backupRecoveryWireDigest), leased.Request)

	var heartbeat Heartbeat
	raw = readWireFixture(t, "heartbeat.json", "a2f948e9582c7d1865a208135253c42f5a5c00484c6aa84842e5c433b6f28f13")
	if err := json.Unmarshal(raw, &heartbeat); err != nil {
		t.Fatal(err)
	}
	if heartbeat.LeaseToken != leased.LeaseToken || heartbeat.Phase != "checking" || heartbeat.Message != "Inspecting original restore" {
		t.Fatal("saved heartbeat lost its active lease or progress")
	}
	checkWireEncoding(t, raw, heartbeat)

	var completion Completion
	raw = readWireFixture(t, "backup-recovery-completion.json", "ca60a8cdcc615c06efe0303cb500dbb04b38f82a64f12e303bdc1adc824aec8c")
	if err := json.Unmarshal(raw, &completion); err != nil {
		t.Fatal(err)
	}
	if completion.LeaseToken != leased.LeaseToken || completion.Result.State != "succeeded" || completion.Result.WorkloadBackup == nil || completion.Result.WorkloadBackup.OperationID != "fixture-original-restore" || completion.Result.WorkloadBackup.CleanupState != "complete" {
		t.Fatal("saved completion lost its original operation or confirmed cleanup")
	}
	if err := leased.Request.ValidateWorkloadBackupResult(completion.Result); err != nil {
		t.Fatalf("saved recovery completion no longer validates: %v", err)
	}
	checkWireEncoding(t, raw, completion)
}
