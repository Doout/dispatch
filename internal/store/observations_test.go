package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestObservationPersistence(t *testing.T) {
	testObservationPersistence(t, filepath.Join(t.TempDir(), "observations.db"))
}
func TestObservationPostgresPersistenceIntegration(t *testing.T) {
	dsn := os.Getenv("DISPATCH_OBSERVATIONS_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_OBSERVATIONS_POSTGRES_URL to a disposable database")
	}
	testObservationPersistence(t, dsn)
}
func testObservationPersistence(t *testing.T, dsn string) {
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 2; i++ {
		if err = s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	project := core.Project{ID: "observation-project", Name: "observation-project", CreatedAt: now}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateServer(ctx, core.Server{ID: "observation-server", Name: "observation-server", Runtime: "docker", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	app := core.App{ID: "observation-app", Name: "observation-app", ProjectID: project.ID, ServerID: "observation-server", BuildType: core.BuildTypeDockerfile, CreatedAt: now}
	if err = s.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	c := core.ObservationConfig{AppID: app.ID, ProjectID: app.ProjectID, Revision: 1, IntervalSeconds: 60, StaleAfterSeconds: 120, WebhookConfigured: true, WebhookCiphertext: "ciphertext-test", UpdatedAt: now}
	if err = s.SaveObservationConfig(ctx, c, 0); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetObservationConfig(ctx, app.ID)
	if err != nil || loaded.WebhookCiphertext != c.WebhookCiphertext {
		t.Fatal(loaded, err)
	}
	raw, _ := json.Marshal(loaded)
	if strings.Contains(string(raw), "ciphertext-test") {
		t.Fatal("ciphertext leaked")
	}
	if err = s.SaveObservationConfig(ctx, c, 0); !errors.Is(err, ErrObservationConflict) {
		t.Fatal("duplicate did not conflict", err)
	}
	o := core.ApplicationObservation{AppID: app.ID, ProjectID: app.ProjectID, ConfigurationRevision: 1, State: "unhealthy", CheckedAt: &now}
	e := core.ObservationEvent{ID: "event", AppID: app.ID, ProjectID: app.ProjectID, ConfigurationRevision: 1, CreatedAt: now, Delivery: "pending", NextAttemptAt: &now}
	if err = s.SaveObservation(ctx, o, &e); err != nil {
		t.Fatal(err)
	}
	events, err := s.PendingObservationEvents(ctx, now.Add(time.Second))
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	e.Delivery = "delivered"
	e.DeliveredAt = &now
	if err = s.SaveObservationEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateObservationEvent(ctx, core.ObservationEvent{ID: e.ID, AppID: app.ID, ProjectID: app.ProjectID, CreatedAt: now, Delivery: "pending"}); err != nil {
		t.Fatal(err)
	}
	events, _ = s.ListObservationEvents(ctx, app.ID)
	if len(events) != 1 || events[0].Delivery != "delivered" {
		t.Fatal("duplicate event replaced delivery", events)
	}
	c.Revision = 2
	if err = s.SaveObservationConfig(ctx, c, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveObservation(ctx, o, nil); !errors.Is(err, ErrObservationConflict) {
		t.Fatal("outdated observation accepted", err)
	}
	if err = s.DeleteApp(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	events, _ = s.ListObservationEvents(ctx, app.ID)
	if len(events) != 0 {
		t.Fatal("history not cascaded")
	}
	if _, err = s.GetObservationConfig(ctx, app.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
