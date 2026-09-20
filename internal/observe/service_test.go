package observe

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/store"
)

func fixture(t *testing.T) (*Service, *store.SQLStore, core.App) {
	t.Helper()
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, id := range []string{"project", "other"} {
		if err = data.CreateProject(ctx, core.Project{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err = data.CreateServer(ctx, core.Server{ID: "server", Name: "server", Runtime: "kubernetes", Kubernetes: &core.KubernetesServerConfig{Namespace: "default"}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	app := core.App{ID: "app", Name: "app", ProjectID: "project", ServerID: "server", BuildType: core.BuildTypeHelm, CreatedAt: now}
	if err = data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err = data.CreateDeployment(ctx, core.Deployment{ID: "deployment", AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "key")
	if err = os.WriteFile(key, []byte(strings.Repeat("!", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(key)
	if err != nil {
		t.Fatal(err)
	}
	s := New(data, nil, nil, vault)
	s.Now = func() time.Time { return now }
	s.CheckDrift = func(context.Context, string) (core.DriftCheck, error) {
		return core.DriftCheck{DeploymentID: "deployment", State: "synced", Health: "healthy"}, nil
	}
	s.Probe = func(context.Context, string) core.EndpointObservation {
		return core.EndpointObservation{State: "not_configured"}
	}
	return s, data, app
}
func input() ConfigInput {
	return ConfigInput{Scheduled: true, IntervalSeconds: 60, StaleAfterSeconds: 120}
}
func TestObservationDefaultsFreshnessAndNoRepeatedEvents(t *testing.T) {
	s, _, app := fixture(t)
	ctx := context.Background()
	initial, err := s.Status(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Configuration.Scheduled || initial.Configuration.NotificationsEnabled || initial.Freshness != "not_checked" {
		t.Fatal(initial)
	}
	first, err := s.Check(ctx, app.ID, "deployment")
	if err != nil {
		t.Fatal(err)
	}
	if first.State != "healthy" {
		t.Fatal(first)
	}
	status, _ := s.Status(ctx, app.ID)
	if status.Freshness != "fresh" || len(status.Events) != 0 {
		t.Fatal(status)
	}
	now := s.Now().Add(16 * time.Minute)
	s.Now = func() time.Time { return now }
	status, _ = s.Status(ctx, app.ID)
	if status.Freshness != "stale" {
		t.Fatal(status)
	}
	s.CheckDrift = func(context.Context, string) (core.DriftCheck, error) {
		return core.DriftCheck{DeploymentID: "deployment", State: "out_of_sync", Health: "healthy"}, nil
	}
	for i := 0; i < 3; i++ {
		if _, err = s.Check(ctx, app.ID, "manual"); err != nil {
			t.Fatal(err)
		}
	}
	status, _ = s.Status(ctx, app.ID)
	if len(status.Events) != 1 || status.Events[0].State != "unhealthy" {
		t.Fatal(status.Events)
	}
	s.CheckDrift = func(context.Context, string) (core.DriftCheck, error) {
		return core.DriftCheck{DeploymentID: "deployment", State: "synced", Health: "healthy"}, nil
	}
	_, _ = s.Check(ctx, app.ID, "manual")
	status, _ = s.Status(ctx, app.ID)
	if len(status.Events) != 2 || status.Events[0].Kind != "recovered" {
		t.Fatal(status.Events)
	}
}
func TestObservationConfigurationIsolationWriteOnlyAndOptimisticUpdates(t *testing.T) {
	s, data, app := fixture(t)
	ctx := context.Background()
	in := input()
	secret := "https://hooks.example.com/secret-delivery-token"
	in.WebhookURL = &secret
	in.NotificationsEnabled = true
	c, err := s.Configure(ctx, app.ID, "operator", in)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "secret-delivery-token") || strings.Contains(string(raw), c.WebhookCiphertext) {
		t.Fatal("webhook leaked")
	}
	stored, _ := data.GetObservationConfig(ctx, app.ID)
	if stored.WebhookCiphertext == "" || stored.WebhookCiphertext == secret {
		t.Fatal("not encrypted")
	}
	in.Revision = c.Revision
	in.WebhookURL = nil
	c, err = s.Configure(ctx, app.ID, "operator", in)
	if err != nil || c.WebhookCiphertext != stored.WebhookCiphertext {
		t.Fatal("omitted webhook was lost", err)
	}
	if _, err = s.Configure(ctx, app.ID, "operator", in); !errors.Is(err, store.ErrObservationConflict) {
		t.Fatalf("concurrent edit: %v", err)
	}
	in.Revision = c.Revision
	in.RemoveWebhook = true
	in.NotificationsEnabled = false
	c, err = s.Configure(ctx, app.ID, "operator", in)
	if err != nil || c.WebhookConfigured || c.WebhookCiphertext != "" {
		t.Fatal("removal failed", err)
	}
	app.ProjectID = "other"
	if err = data.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Check(ctx, app.ID, "schedule"); err == nil {
		t.Fatal("cross-project settings executed")
	}
	if _, err = s.Status(ctx, app.ID); err == nil {
		t.Fatal("cross-project history exposed")
	}
}
func TestObservationSettingsChangedDuringCheckAreNotApplied(t *testing.T) {
	s, data, app := fixture(t)
	ctx := context.Background()
	in := input()
	s.CheckDrift = func(context.Context, string) (core.DriftCheck, error) {
		_, err := s.Configure(ctx, app.ID, "operator", in)
		if err != nil {
			t.Fatal(err)
		}
		return core.DriftCheck{State: "synced", Health: "healthy"}, nil
	}
	if _, err := s.Check(ctx, app.ID, "manual"); !errors.Is(err, store.ErrObservationConflict) {
		t.Fatal(err)
	}
	if _, err := data.GetObservation(ctx, app.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("outdated configuration saved")
	}
}
func TestObservationNotificationsDedupeBackoffMuteAndRecovery(t *testing.T) {
	s, _, app := fixture(t)
	ctx := context.Background()
	in := input()
	url := "https://hooks.example.com/private-token"
	in.WebhookURL = &url
	in.NotificationsEnabled = true
	c, err := s.Configure(ctx, app.ID, "operator", in)
	if err != nil {
		t.Fatal(err)
	}
	s.CheckDrift = func(context.Context, string) (core.DriftCheck, error) {
		return core.DriftCheck{State: "synced", Health: "degraded"}, nil
	}
	_, _ = s.Check(ctx, app.ID, "manual")
	_, _ = s.Check(ctx, app.ID, "manual")
	calls := 0
	s.Deliver = func(_ context.Context, got string, e core.ObservationEvent) error {
		calls++
		if got != url {
			t.Fatal("wrong URL")
		}
		return errors.New("may include credential " + url)
	}
	if err = s.DeliverPending(ctx); err != nil {
		t.Fatal(err)
	}
	status, _ := s.Status(ctx, app.ID)
	if calls != 1 || len(status.Events) != 1 || status.Events[0].Attempts != 1 || status.Events[0].Delivery != "pending" || strings.Contains(status.Events[0].DeliveryMessage, "private-token") {
		t.Fatal(status.Events, calls)
	}
	_ = s.DeliverPending(ctx)
	if calls != 1 {
		t.Fatal("retry did not respect due time")
	}
	now := s.Now().Add(time.Minute)
	s.Now = func() time.Time { return now }
	s.Deliver = func(context.Context, string, core.ObservationEvent) error { calls++; return nil }
	_ = s.DeliverPending(ctx)
	_ = s.DeliverPending(ctx)
	if calls != 2 {
		t.Fatal("delivery repeated")
	}
	until := now.Add(time.Hour)
	in.Revision = c.Revision
	in.WebhookURL = nil
	in.MutedUntil = &until
	_, err = s.Configure(ctx, app.ID, "operator", in)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Check(ctx, app.ID, "manual")
	_ = s.DeliverPending(ctx)
	status, _ = s.Status(ctx, app.ID)
	if calls != 2 || status.Events[0].Delivery != "muted" {
		t.Fatal(status.Events, calls)
	}
	if backoff(60, 100) != 24*time.Hour || backoff(60, 2) != 2*time.Minute {
		t.Fatal("incorrect backoff")
	}
}
func TestObservationConcurrentChecksDoNotOverlap(t *testing.T) {
	s, _, app := fixture(t)
	started := make(chan struct{})
	finish := make(chan struct{})
	s.CheckDrift = func(context.Context, string) (core.DriftCheck, error) {
		close(started)
		<-finish
		return core.DriftCheck{State: "synced", Health: "healthy"}, nil
	}
	done := make(chan error, 1)
	go func() { _, err := s.Check(context.Background(), app.ID, "manual"); done <- err }()
	<-started
	status, _ := s.Status(context.Background(), app.ID)
	if !status.Checking || status.Freshness != "checking" {
		t.Fatal(status)
	}
	if _, err := s.Check(context.Background(), app.ID, "manual"); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestObservationWorkerChecksAfterDeploymentWithBoundedConcurrency(t *testing.T) {
	s, data, app := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 1; i <= 4; i++ {
		a := app
		a.ID = string(rune('a' + i))
		a.Name = a.ID
		if err := data.CreateApp(ctx, a); err != nil {
			t.Fatal(err)
		}
		d := core.Deployment{ID: a.ID + "-deployment", AppID: a.ID, State: core.DeploymentSucceeded, CreatedAt: s.Now()}
		if err := data.CreateDeployment(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	var running, maximum atomic.Int32
	started := make(chan struct{}, 8)
	s.CheckDrift = func(ctx context.Context, _ string) (core.DriftCheck, error) {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			old := maximum.Load()
			if n <= old || maximum.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		<-ctx.Done()
		return core.DriftCheck{}, ctx.Err()
	}
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	if maximum.Load() != 2 {
		t.Fatal(maximum.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("workers did not stop")
	}
}

func TestObservationRestartRecoversFailedAttemptWithoutDuplicateDelivery(t *testing.T) {
	s, data, app := fixture(t)
	ctx := context.Background()
	in := input()
	hook := "https://hooks.example.com/notify"
	in.WebhookURL = &hook
	in.NotificationsEnabled = true
	if _, err := s.Configure(ctx, app.ID, "operator", in); err != nil {
		t.Fatal(err)
	}
	finished := s.Now().Add(time.Minute)
	d := core.Deployment{ID: "failed-on-shutdown", AppID: app.ID, State: core.DeploymentFailed, CreatedAt: finished, FinishedAt: &finished}
	if err := data.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { s.Run(runCtx); close(done) }()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	found := false
	for !found {
		select {
		case <-deadline:
			cancel()
			t.Fatal("startup did not recover failed attempt")
		case <-ticker.C:
			events, err := data.ListObservationEvents(ctx, app.ID)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			for _, e := range events {
				if e.ID == "deployment:"+d.ID {
					found = true
				}
			}
		}
	}
	cancel()
	<-done
	events, _ := data.ListObservationEvents(ctx, app.ID)
	for _, e := range events {
		if e.Kind == "deployment_recovered" {
			t.Fatal("older success misreported as recovery")
		}
	}
	now := finished.Add(time.Second)
	s.Now = func() time.Time { return now }
	calls := 0
	s.Deliver = func(context.Context, string, core.ObservationEvent) error { calls++; return nil }
	if err := s.DeliverPending(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.completed(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := s.DeliverPending(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("failure notification lost or repeated", calls)
	}
}
