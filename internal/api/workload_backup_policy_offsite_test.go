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
	"github.com/doout/dispatch/internal/observe"
	"github.com/doout/dispatch/internal/store"
)

func scheduledOffsitePolicy(t *testing.T, a *API, source core.ServiceResource, token string, extra map[string]any) core.WorkloadBackupPolicy {
	t.Helper()
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+source.ProjectID, map[string]any{"kind": "target", "resourceId": source.Target.ServerID}, 200)
	var destination core.BackupObjectStore
	registration := offsiteRegistration(t, a, source.ProjectID, "scheduled-recovery")
	json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/workload-backup-stores", registration, 201), &destination)
	input := map[string]any{"name": "scheduled-offsite", "sourceRunId": source.RunID, "intervalHours": 1, "keepLast": 1, "confirmRetention": "scheduled-offsite", "offsiteStoreId": destination.ID, "confirmOffsiteStoreId": destination.ID, "offsiteStaleAfterHours": 2}
	for key, value := range extra {
		input[key] = value
	}
	response := mutationRequest(a, token, "POST", "/api/v1/workload-backup-policies", "scheduled-offsite-policy", input)
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	policy, err := a.store.(store.WorkloadBackupPolicyStore).GetWorkloadBackupPolicy(context.Background(), decodeMutation(t, response).OperationID)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func scheduledOffsiteResult(input core.WorkloadBackupRequest) core.WorkloadBackupResult {
	result := core.WorkloadBackupResult{BackupID: input.Backup.ID, ProjectID: input.Backup.ProjectID, OperationID: input.OperationID, ArtifactID: input.Backup.ID, CleanupState: "complete", Checksum: strings.Repeat("a", 64), Bytes: 42}
	switch input.Action {
	case "backup":
		result.State = "ready"
	case "verify":
		result.State = "verified"
	case "export", "reconcile":
		result.State = "exported"
		result.Offsite = &core.BackupOffsiteArtifact{StoreID: input.OffsiteAccess.StoreID, ArchiveKey: input.OffsiteAccess.Archive.Key, ManifestKey: input.OffsiteAccess.Manifest.Key, ManifestChecksum: strings.Repeat("b", 64), ImageReference: "postgres@sha256:" + strings.Repeat("c", 64), ConfirmedAt: time.Now().UTC()}
	}
	return result
}

func verifyScheduledCapture(t *testing.T, a *API, p core.WorkloadBackupPolicy) core.WorkloadBackup {
	t.Helper()
	ctx := context.Background()
	a.scheduleWorkloadBackupCaptures(ctx)
	p, _ = a.store.(store.WorkloadBackupPolicyStore).GetWorkloadBackupPolicy(ctx, p.ID)
	awaitBackupOperation(t, a, p.LastBackupID, "succeeded")
	a.scheduleWorkloadBackupVerification(ctx)
	ops, _ := a.store.(store.WorkloadBackupStore).ListWorkloadBackupOperations(ctx, p.LastBackupID)
	for _, op := range ops {
		if op.Action == "verify" {
			awaitBackupOperation(t, a, op.ID, "succeeded")
		}
	}
	b, err := a.store.(store.WorkloadBackupStore).GetWorkloadBackup(ctx, p.LastBackupID)
	if err != nil || b.VerificationState != "verified" {
		t.Fatal("capture did not verify", err, b.VerificationState)
	}
	return b
}

func TestScheduledOffsiteExportVerifiesBeforeProtectionAndKeepsOneOperation(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	var mu sync.Mutex
	exports, offsiteVerifications := 0, 0
	a.workloadBackupBackend = func(_ context.Context, input core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if input.Action == "export" {
			exports++
			if input.OffsiteAccess == nil || input.OffsiteAccess.Archive.Put == "" {
				t.Error("export omitted exact object write grant")
			}
		}
		if input.Action == "verify" && input.OffsiteAccess != nil {
			offsiteVerifications++
			if input.OffsiteAccess.Archive.Get == "" || input.OffsiteAccess.Archive.Put != "" {
				t.Error("offsite verification did not use exact read-only access")
			}
		}
		return scheduledOffsiteResult(input), nil
	}
	p := scheduledOffsitePolicy(t, a, source, "secret", nil)
	b := verifyScheduledCapture(t, a, p)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.scheduleWorkloadBackupCaptures(ctx) }()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			serviceRequestTest(t, a, "GET", "/api/v1/workload-backup-policies/"+p.ID, nil, 200)
		}()
	}
	wg.Wait()
	b, _ = a.store.(store.WorkloadBackupStore).GetWorkloadBackup(ctx, b.ID)
	if b.ScheduledExportOperationID == "" {
		t.Fatal("no durable export identity")
	}
	awaitBackupOperation(t, a, b.ScheduledExportOperationID, "succeeded")
	a.scheduleWorkloadBackupCaptures(ctx)
	p, _ = a.store.(store.WorkloadBackupPolicyStore).GetWorkloadBackupPolicy(ctx, p.ID)
	if p.LastOffsiteBackupID != "" || p.OffsiteState != "verifying" {
		t.Fatal("upload alone reported offsite protection", p.LastOffsiteBackupID, p.OffsiteState)
	}
	a.scheduleWorkloadBackupVerification(ctx)
	ops, _ := a.store.(store.WorkloadBackupStore).ListWorkloadBackupOperations(ctx, b.ID)
	for _, op := range ops {
		if op.Action == "verify" && op.OffsiteStoreID != "" {
			awaitBackupOperation(t, a, op.ID, "succeeded")
		}
	}
	a.scheduleWorkloadBackupCaptures(ctx)
	p, _ = a.store.(store.WorkloadBackupPolicyStore).GetWorkloadBackupPolicy(ctx, p.ID)
	if p.LastOffsiteBackupID != b.ID || p.LastOffsiteVerifiedAt == nil || p.LastOffsiteRecoveryPointAt == nil || !p.LastOffsiteRecoveryPointAt.Equal(b.CreatedAt) || p.OffsiteFreshness != "fresh" {
		t.Fatal("verified captured recovery point missing", p.OffsiteState, p.OffsiteFreshness)
	}
	for i := 0; i < 3; i++ {
		a.scheduleWorkloadBackupCaptures(ctx)
	}
	mu.Lock()
	defer mu.Unlock()
	if exports != 1 || offsiteVerifications != 1 {
		t.Fatal("repeated side effects", exports, offsiteVerifications)
	}
	raw := serviceRequestTest(t, a, "GET", "/api/v1/workload-backup-policies/"+p.ID, nil, 200)
	assertOffsitePublic(t, string(raw))
	storedAfterRead, _ := a.store.(store.WorkloadBackupPolicyStore).GetWorkloadBackupPolicy(ctx, p.ID)
	if storedAfterRead.Revision != p.Revision || storedAfterRead.LastBackupID != p.LastBackupID {
		t.Fatal("public inspection mutated capture state")
	}

	if strings.Contains(string(raw), p.EncryptedInput) {
		t.Fatal("policy exposed accepted encrypted input")
	}
	changed := p
	changed.OffsiteStoreID = "replacement"
	if err := a.store.(store.WorkloadBackupPolicyStore).UpdateWorkloadBackupPolicy(ctx, changed, p.Revision); err == nil {
		t.Fatal("immutable destination changed")
	}
}

func TestScheduledOffsiteExportLostReplyInspectsOriginalWithoutReexport(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	var mu sync.Mutex
	exports, inspections := 0, 0
	var original string
	a.workloadBackupBackend = func(_ context.Context, input core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
		mu.Lock()
		defer mu.Unlock()
		result := scheduledOffsiteResult(input)
		if input.Action == "export" {
			exports++
			original = input.OperationID
			result.State, result.Offsite = "unknown", nil
			return result, errors.New("uncertain export reply")
		}
		if input.Action == "reconcile" {
			inspections++
			if input.OperationID != original || input.RecoveryAction != "export" || input.OffsiteAccess.Archive.Put != "" {
				t.Error("recovery replayed export or changed identity")
			}
		}
		return result, nil
	}
	p := scheduledOffsitePolicy(t, a, source, "secret", nil)
	b := verifyScheduledCapture(t, a, p)
	a.scheduleWorkloadBackupCaptures(ctx)
	b, _ = a.store.(store.WorkloadBackupStore).GetWorkloadBackup(ctx, b.ID)
	awaitBackupOperation(t, a, b.ScheduledExportOperationID, "unknown")
	for i := 0; i < 3; i++ {
		a.scheduleWorkloadBackupCaptures(ctx)
	}
	claimed, err := a.store.(store.WorkloadBackupStore).ClaimWorkloadBackupRecovery(ctx, b.ScheduledExportOperationID, time.Now().Add(32*time.Minute), "replacement-controller")
	if err != nil {
		t.Fatal(err)
	}
	a.executeWorkloadBackupOperation(claimed, true)
	awaitBackupOperation(t, a, b.ScheduledExportOperationID, "succeeded")
	a.scheduleWorkloadBackupCaptures(ctx)
	mu.Lock()
	defer mu.Unlock()
	if exports != 1 || inspections != 1 {
		t.Fatal("uncertain export repeated", exports, inspections)
	}
}

func TestScheduledOffsiteRevokedAuthorityAndRejectedExportCountOnce(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	token, _ := offsiteIdentity(t, a, source.ProjectID)
	exports := 0
	a.workloadBackupBackend = func(_ context.Context, input core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
		if input.Action == "export" {
			exports++
		}
		return scheduledOffsiteResult(input), nil
	}
	p := scheduledOffsitePolicy(t, a, source, token, nil)
	b := verifyScheduledCapture(t, a, p)
	if err := a.store.(store.AutomationStore).RevokeAutomationCredential(ctx, p.Actor.ID, p.Actor.CredentialID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		a.scheduleWorkloadBackupCaptures(ctx)
	}
	p, _ = a.store.(store.WorkloadBackupPolicyStore).GetWorkloadBackupPolicy(ctx, p.ID)
	b, _ = a.store.(store.WorkloadBackupStore).GetWorkloadBackup(ctx, b.ID)
	if exports != 0 || p.MissedExports != 1 || !b.ScheduledExportMissed || b.ScheduledExportOperationID != "" || p.OffsiteState != "blocked" {
		t.Fatal("revoked authority or missed-export dedup failed", exports, p.MissedExports, p.OffsiteState)
	}
}

func TestScheduledOffsiteApprovalAndNotificationScope(t *testing.T) {
	a, source := backupAPIFixture(t)
	var destination core.BackupObjectStore
	json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/workload-backup-stores", offsiteRegistration(t, a, source.ProjectID, "approved"), 201), &destination)
	base := map[string]any{"name": "approval-test", "sourceRunId": source.RunID, "intervalHours": 1, "keepLast": 1, "confirmRetention": "approval-test", "offsiteStoreId": destination.ID}
	response := mutationRequest(a, "secret", "POST", "/api/v1/workload-backup-policies", "missing-store-approval", base)
	if response.Code != 422 {
		t.Fatal("missing approval accepted", response.Code)
	}
	base["confirmOffsiteStoreId"] = destination.ID
	base["notificationAppId"] = "unknown-app"
	response = mutationRequest(a, "secret", "POST", "/api/v1/workload-backup-policies", "missing-notify-optin", base)
	if response.Code != 409 {
		t.Fatal("unconfigured delivery channel accepted", response.Code)
	}
	foreign := core.Project{ID: "foreign-store-project", Name: "Foreign", CreatedAt: time.Now().UTC()}
	if err := a.store.CreateProject(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	var foreignStore core.BackupObjectStore
	json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/workload-backup-stores", offsiteRegistration(t, a, foreign.ID, "foreign"), 201), &foreignStore)
	delete(base, "notificationAppId")
	base["offsiteStoreId"], base["confirmOffsiteStoreId"] = foreignStore.ID, foreignStore.ID
	response = mutationRequest(a, "secret", "POST", "/api/v1/workload-backup-policies", "foreign-store-policy", base)
	if response.Code != 409 {
		t.Fatal("foreign destination accepted", response.Code)
	}
}

func finishScheduledOffsite(t *testing.T, a *API, b core.WorkloadBackup) core.WorkloadBackup {
	t.Helper()
	ctx := context.Background()
	a.scheduleWorkloadBackupCaptures(ctx)
	b, _ = a.store.(store.WorkloadBackupStore).GetWorkloadBackup(ctx, b.ID)
	awaitBackupOperation(t, a, b.ScheduledExportOperationID, "succeeded")
	a.scheduleWorkloadBackupVerification(ctx)
	ops, _ := a.store.(store.WorkloadBackupStore).ListWorkloadBackupOperations(ctx, b.ID)
	found := false
	for _, op := range ops {
		if op.Action == "verify" && op.OffsiteStoreID != "" {
			found = true
			awaitBackupOperation(t, a, op.ID, "succeeded")
		}
	}
	if !found {
		t.Fatal("confirmed export did not schedule immediate offsite verification")
	}
	b, _ = a.store.(store.WorkloadBackupStore).GetWorkloadBackup(ctx, b.ID)
	return b
}

func TestScheduledOffsiteLocalRetentionRequiresApprovalAndVerifiedNewPoint(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "automatic"}[automatic], func(t *testing.T) {
			a, source := backupAPIFixture(t)
			ctx := context.Background()
			var mu sync.Mutex
			retired, removedOffsite := 0, 0
			a.workloadBackupBackend = func(_ context.Context, input core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
				mu.Lock()
				defer mu.Unlock()
				if input.Action == "retire-local" {
					retired++
					if input.OffsiteAccess == nil || input.OffsiteAccess.Archive.Put != "" || input.Backup.Offsite.VerificationState != "verified" {
						t.Error("retirement lacked verified exact offsite access")
					}
					return core.WorkloadBackupResult{State: "local-retired", CleanupState: "complete"}, nil
				}
				if input.Action == "delete-offsite" || input.Action == "delete" {
					removedOffsite++
				}
				return scheduledOffsiteResult(input), nil
			}
			p := scheduledOffsitePolicy(t, a, source, "secret", map[string]any{"retireLocalAfterOffsiteVerification": automatic})
			first := finishScheduledOffsite(t, a, verifyScheduledCapture(t, a, p))
			a.scheduleWorkloadBackupCaptures(ctx)
			p, _ = a.store.(store.WorkloadBackupPolicyStore).GetWorkloadBackupPolicy(ctx, p.ID)
			p.NextCaptureAt = time.Now().UTC().Add(-time.Second)
			if err := a.store.(store.WorkloadBackupPolicyStore).UpdateWorkloadBackupPolicy(ctx, p, p.Revision); err != nil {
				t.Fatal(err)
			}
			second := finishScheduledOffsite(t, a, verifyScheduledCapture(t, a, p))
			a.scheduleWorkloadBackupCaptures(ctx)
			if automatic {
				ops, _ := a.store.(store.WorkloadBackupStore).ListWorkloadBackupOperations(ctx, first.ID)
				found := false
				for _, op := range ops {
					if op.Action == "retire-local" {
						found = true
						awaitBackupOperation(t, a, op.ID, "succeeded")
					}
				}
				if !found {
					t.Fatal("approved older local copy did not retire")
				}
			}
			for i := 0; i < 3; i++ {
				a.scheduleWorkloadBackupCaptures(ctx)
			}
			first, _ = a.store.(store.WorkloadBackupStore).GetWorkloadBackup(ctx, first.ID)
			second, _ = a.store.(store.WorkloadBackupStore).GetWorkloadBackup(ctx, second.ID)
			if first.State != "ready" || first.EncryptedInput == "" || first.Offsite == nil || first.Offsite.VerificationState != "verified" || second.LocalState == "retired" {
				t.Fatal("retention removed recovery metadata or newest local copy")
			}
			p, _ = a.store.(store.WorkloadBackupPolicyStore).GetWorkloadBackupPolicy(ctx, p.ID)
			if !automatic && p.RetentionBlockedReason == "" {
				t.Fatal("unapproved local retention was silently healthy")
			}
			mu.Lock()
			defer mu.Unlock()
			want := 0
			if automatic {
				want = 1
			}
			if retired != want || removedOffsite != 0 {
				t.Fatal("retention exceeded approved scope", retired, removedOffsite)
			}
		})
	}
}

func TestScheduledOffsiteNotificationsUseDurableOptInAndSanitizedFailures(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	apps, _ := a.store.ListApps(ctx)
	var app core.App
	for _, candidate := range apps {
		if candidate.ProjectID == source.ProjectID {
			app = candidate
			break
		}
	}
	if app.ID == "" {
		t.Fatal("notification fixture application missing")
	}
	const privateURL = "https://hooks.example.com/private-policy-token"
	url := privateURL
	_, err := a.observations.Configure(ctx, app.ID, "operator", observe.ConfigInput{IntervalSeconds: 300, StaleAfterSeconds: 900, NotificationsEnabled: true, WebhookURL: &url})
	if err != nil {
		t.Fatal(err)
	}
	a.workloadBackupBackend = func(_ context.Context, input core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
		if input.Action == "export" {
			return core.WorkloadBackupResult{State: "failed", CleanupState: "complete"}, errors.New("private error " + privateURL + " " + input.Key)
		}
		return scheduledOffsiteResult(input), nil
	}
	p := scheduledOffsitePolicy(t, a, source, "secret", map[string]any{"notificationAppId": app.ID})
	b := verifyScheduledCapture(t, a, p)
	a.scheduleWorkloadBackupCaptures(ctx)
	b, _ = a.store.(store.WorkloadBackupStore).GetWorkloadBackup(ctx, b.ID)
	awaitBackupOperation(t, a, b.ScheduledExportOperationID, "failed")
	for i := 0; i < 4; i++ {
		a.scheduleWorkloadBackupCaptures(ctx)
	}
	p, _ = a.store.(store.WorkloadBackupPolicyStore).GetWorkloadBackupPolicy(ctx, p.ID)
	events, err := a.observations.Repo.ListObservationEvents(ctx, app.ID)
	if err != nil || len(events) != 1 || events[0].Kind != "backup_offsite" || events[0].Delivery != "pending" || p.MissedExports != 1 {
		t.Fatal("failure notification duplicated or missing", len(events), p.MissedExports, err)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "private-policy-token") || strings.Contains(string(raw), "fixture-signing-secret") {
		t.Fatal("notification leaked credentials")
	}
	calls := 0
	a.observations.Deliver = func(_ context.Context, destination string, event core.ObservationEvent) error {
		calls++
		if destination != privateURL || event.ID != events[0].ID {
			t.Error("notification changed destination or event identity")
		}
		if calls == 1 {
			return errors.New("transport failure containing " + privateURL)
		}
		return nil
	}
	if err = a.observations.DeliverPending(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.observations.DeliverPending(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("outbox ignored retry deadline")
	}
	next := time.Now().UTC().Add(2 * time.Minute)
	a.observations.Now = func() time.Time { return next }
	if err = a.observations.DeliverPending(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.observations.DeliverPending(ctx); err != nil {
		t.Fatal(err)
	}
	events, _ = a.observations.Repo.ListObservationEvents(ctx, app.ID)
	if calls != 2 || len(events) != 1 || events[0].Delivery != "delivered" {
		t.Fatal("durable delivery repeated or did not recover", calls)
	}
}

func TestScheduledOffsiteNotificationsKeepDistinctRecurringTransitions(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	apps, _ := a.store.ListApps(ctx)
	var app core.App
	for _, candidate := range apps {
		if candidate.ProjectID == source.ProjectID {
			app = candidate
			break
		}
	}
	url := "https://hooks.example.com/recurring-backup"
	if _, err := a.observations.Configure(ctx, app.ID, "operator", observe.ConfigInput{IntervalSeconds: 300, StaleAfterSeconds: 900, NotificationsEnabled: true, WebhookURL: &url}); err != nil {
		t.Fatal(err)
	}
	p := scheduledOffsitePolicy(t, a, source, "secret", map[string]any{"notificationAppId": app.ID})
	policies := a.store.(store.WorkloadBackupPolicyStore)
	ids := map[string]bool{}
	for _, state := range []string{"blocked", "fresh", "blocked"} {
		p.OffsiteState = state
		p.OffsiteFreshness = state
		if state == "fresh" {
			p.OffsiteState = "healthy"
		}
		event := a.backupPolicyOffsiteEvent(ctx, &p)
		if event == nil || ids[event.ID] {
			t.Fatal("recurring actionable transition lost its durable event identity", state)
		}
		ids[event.ID] = true
		if err := policies.UpdateWorkloadBackupPolicyEvent(ctx, p, p.Revision, event); err != nil {
			t.Fatal(err)
		}
		p, _ = policies.GetWorkloadBackupPolicy(ctx, p.ID)
		if duplicate := a.backupPolicyOffsiteEvent(ctx, &p); duplicate != nil {
			t.Fatal("unchanged protection sent another event")
		}
	}
	events, err := a.observations.Repo.ListObservationEvents(ctx, app.ID)
	if err != nil || len(events) != 3 {
		t.Fatal("outbox dedup erased a later failure episode", len(events), err)
	}
}
