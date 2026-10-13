package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestAutomaticHistoryRetentionAndExpiredComparison(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	now := time.Now().UTC()
	apps, err := a.store.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app := apps[0]
	policy := core.RetentionPolicy{ProjectID: app.ProjectID, LogDays: 14, SnapshotDays: 30, RunDays: 90, KeepRuns: 5, KeepRollbackRevisions: 5}
	data := a.store.(interface {
		SaveRetentionPolicy(context.Context, core.RetentionPolicy) error
	})
	if err = data.SaveRetentionPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		at := now.AddDate(0, 0, -40).Add(time.Duration(i) * time.Hour)
		id := time.Unix(int64(i), 0).Format("150405")
		if err = a.store.CreateDeployment(ctx, core.Deployment{ID: id, AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: at, FinishedAt: &at, Snapshot: core.DeploymentSnapshot{TargetID: app.ServerID, Values: map[string]any{"replicas": i}}}); err != nil {
			t.Fatal(err)
		}
	}
	a.runHistoryRetention(ctx, now)
	expired, err := a.store.GetDeployment(ctx, "000000")
	if err != nil {
		t.Fatal(err)
	}
	if expired.InputsExpiredAt == nil {
		t.Fatal("automatic cleanup did not expire old inputs")
	}
	response := httptest.NewRecorder()
	a.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/deployments/000007/compare?from=000000&basis=inputs", nil))
	var comparison deploymentComparison
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &comparison) != nil || comparison.Available || len(comparison.Changes) != 0 {
		t.Fatal("expired inputs produced a false comparison")
	}
	for _, suffix := range []string{"/topology?values=chart", "/manifests", "/resources/Deployment/example"} {
		response := httptest.NewRecorder()
		a.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/deployments/000000"+suffix, nil))
		if response.Code != http.StatusConflict {
			t.Fatalf("expired resource inspection used current settings: %s status %d", suffix, response.Code)
		}
	}
	pin := httptest.NewRecorder()
	a.ServeHTTP(pin, tokenRequest(http.MethodPut, "/api/v1/deployments/000000/retention-pin", strings.NewReader(`{"pinned":true}`)))
	if pin.Code != 409 {
		t.Fatalf("expired pin status %d", pin.Code)
	}
	policy.PauseAutomatic = true
	if err = data.SaveRetentionPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	at := now.AddDate(0, 0, -40)
	if err = a.store.CreateDeployment(ctx, core.Deployment{ID: "paused-history", AppID: app.ID, State: core.DeploymentFailed, CreatedAt: at, Snapshot: core.DeploymentSnapshot{TargetID: app.ServerID}}); err != nil {
		t.Fatal(err)
	}
	a.runHistoryRetention(ctx, now)
	d, err := a.store.GetDeployment(ctx, "paused-history")
	if err != nil || d.InputsExpiredAt != nil {
		t.Fatal("paused automatic cleanup removed inputs", err)
	}
}
