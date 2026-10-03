package api

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

type backupPolicyInspectionCountingStore struct {
	store.Store
	store.AutomationStore
	// These nil interfaces reject any policy or backup write during HTTP inspection.
	store.WorkloadBackupPolicyStore
	store.WorkloadBackupStore
	policies        []core.WorkloadBackupPolicy
	backups         map[string][]core.WorkloadBackup
	operations      map[string][]core.WorkloadBackupOperation
	backupReads     map[string]int
	operationReads  map[string]int
	backupErrors    map[string]error
	operationErrors map[string]error
}

func (s *backupPolicyInspectionCountingStore) ListWorkloadBackupPolicies(_ context.Context, project string) ([]core.WorkloadBackupPolicy, error) {
	result := []core.WorkloadBackupPolicy{}
	for _, p := range s.policies {
		if project == "" || p.ProjectID == project {
			result = append(result, p)
		}
	}
	return result, nil
}

func (s *backupPolicyInspectionCountingStore) GetWorkloadBackupPolicy(_ context.Context, id string) (core.WorkloadBackupPolicy, error) {
	for _, p := range s.policies {
		if p.ID == id {
			return p, nil
		}
	}
	return core.WorkloadBackupPolicy{}, store.ErrNotFound
}

func (s *backupPolicyInspectionCountingStore) ListWorkloadBackups(_ context.Context, project string) ([]core.WorkloadBackup, error) {
	s.backupReads[project]++
	return s.backups[project], s.backupErrors[project]
}

func (s *backupPolicyInspectionCountingStore) ListWorkloadBackupOperations(_ context.Context, id string) ([]core.WorkloadBackupOperation, error) {
	s.operationReads[id]++
	return s.operations[id], s.operationErrors[id]
}

func (s *backupPolicyInspectionCountingStore) resetReads() {
	s.backupReads, s.operationReads = map[string]int{}, map[string]int{}
}

func backupPolicyInspectionFixture(t *testing.T) (*API, *backupPolicyInspectionCountingStore, string) {
	t.Helper()
	a := serviceTestAPI(t)
	now := time.Now().UTC()
	for _, id := range []string{"inspection-a", "inspection-b", "inspection-hidden", "inspection-local"} {
		if err := a.store.CreateProject(context.Background(), core.Project{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	token, grant := offsiteIdentity(t, a, "inspection-a")
	for _, id := range []string{"inspection-b", "inspection-local"} {
		grant.ProjectID = id
		automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	}
	policy := func(id, project string) core.WorkloadBackupPolicy {
		point := now.Add(-24 * time.Hour)
		return core.WorkloadBackupPolicy{
			ID: id, ProjectID: project, Name: id, OffsiteStoreID: "inspection-destination", OffsiteStaleAfterHours: 2,
			Enabled: true, Revision: 13, State: "scheduled", LastBackupID: id + "-latest", CreatedAt: point,
			OffsiteState: "pending", OffsiteFreshness: "recorded-freshness", OffsiteMessage: "Recorded pending export.",
			LastOffsiteBackupID: "recorded-point", LastOffsiteRecoveryPointAt: &point, RetentionBlockedReason: "recorded blocker",
			RetireLocalAfterOffsiteVerification: true, EncryptedInput: "encrypted-inspection-policy-input",
		}
	}
	hidden := policy("hidden", "inspection-hidden")
	fresh := policy("fresh", "inspection-a")
	local := policy("local", "inspection-local")
	local.OffsiteStoreID = ""
	deleting := policy("deleting", "inspection-a")
	authority := policy("authority", "inspection-a")
	authority.State, authority.OffsiteState, authority.OffsiteMessage = "blocked", "blocked", "The original actor no longer authorizes unattended offsite work."
	stale := policy("stale", "inspection-b")
	cleanup := policy("cleanup", "inspection-a")
	backup := func(p core.WorkloadBackupPolicy, age time.Duration) core.WorkloadBackup {
		captured := now.Add(-age)
		return core.WorkloadBackup{
			ID: p.LastBackupID, ProjectID: p.ProjectID, CapturePolicyID: p.ID, State: "ready", CleanupState: "complete", LocalState: "present", CreatedAt: captured,
			Checksum: strings.Repeat("a", 64), EncryptedInput: "encrypted-inspection-backup-input", Revision: 7,
			Offsite: &core.BackupOffsiteArtifact{StoreID: p.OffsiteStoreID, ConfirmedAt: now, ManifestChecksum: strings.Repeat("b", 64), VerificationState: "verified", VerifiedAt: &now},
		}
	}
	older := backup(deleting, 4*time.Hour)
	older.ID = "deleting-older"
	incomplete := backup(cleanup, time.Hour)
	incomplete.CleanupState = "failed"
	manual := backup(fresh, time.Hour)
	manual.ID, manual.CapturePolicyID = "manual-backup", ""
	unlisted := backup(fresh, time.Hour)
	unlisted.ID, unlisted.CapturePolicyID = "unlisted-backup", "unlisted-policy"
	s := &backupPolicyInspectionCountingStore{
		Store: a.store, AutomationStore: a.store.(store.AutomationStore),
		policies: []core.WorkloadBackupPolicy{hidden, fresh, local, deleting, authority, stale, cleanup},
		backups: map[string][]core.WorkloadBackup{
			"inspection-a":      {backup(fresh, time.Hour), backup(deleting, time.Hour), older, backup(authority, time.Hour), incomplete, manual, unlisted},
			"inspection-b":      {backup(stale, 6*time.Hour)},
			"inspection-hidden": {backup(hidden, time.Hour)},
			"inspection-local":  {backup(local, time.Hour)},
		},
		operations: map[string][]core.WorkloadBackupOperation{
			"deleting-latest": {{ID: "pending-removal", Action: "delete-offsite", State: "unknown"}},
			"cleanup-latest":  {{ID: "incomplete-restore", Action: "restore", State: "unknown", CleanupState: "failed"}},
		},
		backupErrors: map[string]error{}, operationErrors: map[string]error{},
	}
	s.resetReads()
	a.store = s
	a.workloadBackupBackend = func(context.Context, core.WorkloadBackupRequest, core.Server) (core.WorkloadBackupResult, error) {
		t.Error("HTTP inspection dispatched runtime work")
		return core.WorkloadBackupResult{}, errors.New("inspection must be read-only")
	}
	return a, s, token
}

func inspectionPublicPolicy(t *testing.T, p core.WorkloadBackupPolicy) core.WorkloadBackupPolicy {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var result core.WorkloadBackupPolicy
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBackupPolicyInspectionBatchesOnlyVisibleProjects(t *testing.T) {
	a, s, token := backupPolicyInspectionFixture(t)
	before, _ := json.Marshal([]any{s.policies, s.backups, s.operations})
	for _, actor := range []struct {
		name, token string
		owner       bool
	}{{"member", token, false}, {"owner", "secret", true}} {
		t.Run(actor.name, func(t *testing.T) {
			s.resetReads()
			response := automationRequest(t, a, actor.token, "GET", "/api/v1/workload-backup-policies", nil, 200)
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("policy list may be cached")
			}
			assertOffsitePublic(t, response.Body.String())
			var policies []core.WorkloadBackupPolicy
			if err := json.Unmarshal(response.Body.Bytes(), &policies); err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			byID := map[string]core.WorkloadBackupPolicy{}
			for _, p := range policies {
				ids = append(ids, p.ID)
				byID[p.ID] = p
				if p.Revision != 13 || p.EncryptedInput != "" {
					t.Fatal("inspection changed a revision or exposed accepted input", p.ID)
				}
			}
			wantIDs := []string{"fresh", "local", "deleting", "authority", "stale", "cleanup"}
			wantReads := map[string]int{"inspection-a": 1, "inspection-b": 1}
			wantOperations := map[string]int{"fresh-latest": 1, "deleting-latest": 1, "deleting-older": 1, "authority-latest": 1, "stale-latest": 1, "cleanup-latest": 1}
			if actor.owner {
				wantIDs = append([]string{"hidden"}, wantIDs...)
				wantReads["inspection-hidden"], wantOperations["hidden-latest"] = 1, 1
			}
			if !reflect.DeepEqual(ids, wantIDs) || !reflect.DeepEqual(s.backupReads, wantReads) || !reflect.DeepEqual(s.operationReads, wantOperations) {
				t.Fatal("visibility, ordering or read batching changed", ids, s.backupReads, s.operationReads)
			}
			for id, want := range map[string]struct{ state, freshness, point string }{
				"fresh":     {"healthy", "fresh", "fresh-latest"},
				"deleting":  {"blocked", "stale", "deleting-older"},
				"authority": {"blocked", "fresh", "authority-latest"},
				"stale":     {"healthy", "stale", "stale-latest"},
				"cleanup":   {"blocked", "stale", ""},
			} {
				p := byID[id]
				if p.OffsiteState != want.state || p.OffsiteFreshness != want.freshness || p.LastOffsiteBackupID != want.point {
					t.Fatal("current recovery evidence changed", id, p.OffsiteState, p.OffsiteFreshness, p.LastOffsiteBackupID)
				}
			}
			if !strings.Contains(byID["cleanup"].OffsiteMessage, "incomplete cleanup") || byID["authority"].OffsiteMessage != s.policies[4].OffsiteMessage || byID["authority"].RetentionBlockedReason == "" || byID["fresh"].RetentionBlockedReason != "" {
				t.Fatal("cleanup, authority or retention blocker changed")
			}
			wantLocal := inspectionPublicPolicy(t, s.policies[2])
			wantLocal.OffsiteFreshness = "disabled"
			if !reflect.DeepEqual(byID["local"], wantLocal) {
				t.Fatal("local-only policy inspection changed unrelated fields")
			}
		})
	}
	after, _ := json.Marshal([]any{s.policies, s.backups, s.operations})
	if string(before) != string(after) {
		t.Fatal("inspection changed stored policies or archive evidence")
	}
}

func TestBackupPolicyInspectionPreservesReadErrorFallback(t *testing.T) {
	a, s, token := backupPolicyInspectionFixture(t)
	older := s.backups["inspection-a"][0]
	older.ID = "fresh-older"
	s.backups["inspection-a"] = append(s.backups["inspection-a"], older)
	for _, failure := range []string{"backup-list", "operations"} {
		t.Run(failure, func(t *testing.T) {
			s.resetReads()
			s.backupErrors, s.operationErrors = map[string]error{}, map[string]error{}
			if failure == "backup-list" {
				s.backupErrors["inspection-a"] = errors.New("archive lookup unavailable")
			} else {
				s.operationErrors["fresh-latest"] = errors.New("operation lookup unavailable")
			}
			response := automationRequest(t, a, token, "GET", "/api/v1/workload-backup-policies", nil, 200)
			var result []core.WorkloadBackupPolicy
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			for _, p := range result {
				if p.ID == "fresh" || failure == "backup-list" && p.ProjectID == "inspection-a" {
					original, _ := s.GetWorkloadBackupPolicy(context.Background(), p.ID)
					if !reflect.DeepEqual(p, inspectionPublicPolicy(t, original)) {
						t.Fatal("read failure recomputed the recorded projection", p.ID)
					}
				} else if p.ID == "cleanup" && (p.OffsiteState != "blocked" || p.LastOffsiteBackupID != "") || p.ID == "stale" && (p.OffsiteState != "healthy" || p.OffsiteFreshness != "stale") {
					t.Fatal("one policy's read failure suppressed another policy's current evidence", p.ID)
				}
			}
			if s.backupReads["inspection-a"] != 1 || s.backupReads["inspection-b"] != 1 || s.backupReads["inspection-hidden"] != 0 {
				t.Fatal("failed lookup was repeated or inspected hidden project", s.backupReads)
			}
			if failure == "backup-list" && len(s.operationReads) != 1 || failure == "operations" && s.operationReads["fresh-latest"] != 1 {
				t.Fatal("read failure retried or dispatched unrelated operation reads", s.operationReads)
			}
			if s.operationReads["fresh-older"] != 0 {
				t.Fatal("inspection continued reading a policy after its evidence lookup failed")
			}
		})
	}
}

func TestBackupPolicyInspectionSingleReadRefreshesPendingRemoval(t *testing.T) {
	a, s, token := backupPolicyInspectionFixture(t)
	path := "/api/v1/workload-backup-policies/fresh"
	for _, pending := range []bool{false, true} {
		s.resetReads()
		if pending {
			s.operations["fresh-latest"] = []core.WorkloadBackupOperation{{ID: "new-removal", Action: "retire-local", State: "unresolved"}}
		}
		response := automationRequest(t, a, token, "GET", path, nil, 200)
		var p core.WorkloadBackupPolicy
		if err := json.Unmarshal(response.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(s.backupReads, map[string]int{"inspection-a": 1}) || !reflect.DeepEqual(s.operationReads, map[string]int{"fresh-latest": 1}) {
			t.Fatal("single policy inspected sibling policies or repeated reads", s.backupReads, s.operationReads)
		}
		if !pending && (p.OffsiteState != "healthy" || p.LastOffsiteBackupID != "fresh-latest") || pending && (p.OffsiteState != "blocked" || p.LastOffsiteBackupID != "") {
			t.Fatal("inspection reused evidence from an earlier request", p.OffsiteState, p.LastOffsiteBackupID)
		}
	}
	s.resetReads()
	automationRequest(t, a, token, "GET", "/api/v1/workload-backup-policies/hidden", nil, 403)
	automationRequest(t, a, token, "GET", "/api/v1/workload-backup-policies?projectId=inspection-hidden", nil, 403)
	if len(s.backupReads) != 0 || len(s.operationReads) != 0 {
		t.Fatal("forbidden inspection read hidden archive evidence")
	}
}
