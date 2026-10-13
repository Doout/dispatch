package store

import (
	"context"
	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
	"path/filepath"
	"testing"
	"time"
)

func TestPollRetryPersistence(t *testing.T) {
	testPollRetryPersistence(t, filepath.Join(t.TempDir(), "retry.db"))
}
func TestPollRetryPersistencePostgres(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_STORE_POSTGRES_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	testPollRetryPersistence(t, dsn)
}
func testPollRetryPersistence(t *testing.T, dsn string) {
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Migrate(ctx))
	now := time.Now().UTC()
	must(s.CreateProject(ctx, core.Project{ID: "p", Name: "p", CreatedAt: now}))
	for i, want := range []time.Duration{30, 60, 120, 240, 480, 900, 900} {
		at := now.Add(time.Duration(i) * time.Hour)
		next := at.Add(30 * time.Second)
		check := core.EventActivity{ID: ulid.Make().String(), ProjectID: "p", RuleID: "repo", Transport: "poll", Kind: "preview_check", State: "failed", Message: "GitHub returned 503", Check: true, CreatedAt: at, NextAttemptAt: &next}
		must(s.SavePollCheck(ctx, "repo", check))
		saved, err := s.GetPollCheck(ctx, "p", "repo")
		must(err)
		if saved.Attempts != i+1 || saved.NextAttemptAt.Sub(at) != want*time.Second {
			t.Fatalf("bad retry schedule: %+v", saved)
		}
	}
	// Reopening proves retry state survives a controller restart.
	must(s.Close())
	s, err = Open(ctx, dsn)
	must(err)
	defer s.Close()
	check, err := s.GetPollCheck(ctx, "p", "repo")
	must(err)
	if check.Attempts != 7 || check.NextAttemptAt == nil {
		t.Fatal("retry schedule not persisted")
	}
	history, err := s.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{"p"}})
	must(err)
	if len(history) != 1 {
		t.Fatalf("unchanged outage flooded history: %d", len(history))
	}
	check.ID = ulid.Make().String()
	check.State = "processed"
	check.Message = ""
	check.CreatedAt = now.Add(8 * time.Hour)
	must(s.SavePollCheck(ctx, "repo", check))
	recovered, err := s.GetPollCheck(ctx, "p", "repo")
	must(err)
	if recovered.Attempts != 0 || recovered.NextAttemptAt != nil {
		t.Fatal("recovery did not clear retry state")
	}
	history, err = s.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{"p"}})
	must(err)
	if len(history) != 2 || history[0].Kind != "poll_recovered" {
		t.Fatal("recovery not journaled", history)
	}
}
