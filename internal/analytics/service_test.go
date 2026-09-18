package analytics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type memorySource struct {
	mu      sync.Mutex
	events  []core.AnalyticsEvent
	failAck bool
}

func (s *memorySource) ListAnalyticsEvents(context.Context, int) ([]core.AnalyticsEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]core.AnalyticsEvent(nil), s.events...), nil
}
func (s *memorySource) AckAnalyticsEvents(_ context.Context, events []core.AnalyticsEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failAck {
		return errors.New("ack unavailable")
	}
	s.events = nil
	return nil
}
func TestExportRetryRecoveryAndProjectIsolation(t *testing.T) {
	now := time.Now().UTC()
	source := &memorySource{failAck: true, events: []core.AnalyticsEvent{
		{ID: 1, Kind: "workflow", EntityID: "one", ProjectID: "allowed", State: "failed", StartedAt: now.Add(-time.Minute), FinishedAt: now},
		{ID: 2, Kind: "workflow", EntityID: "one", ProjectID: "allowed", State: "succeeded", StartedAt: now.Add(-time.Minute), FinishedAt: now},
		{ID: 3, Kind: "job", EntityID: "secret-project-job", ProjectID: "hidden", State: "succeeded", StartedAt: now, FinishedAt: now, Reused: true},
	}}
	dir := t.TempDir()
	service := New(source, dir, nil)
	if err := service.run(context.Background()); err == nil {
		t.Fatal("expected ack failure")
	}
	if len(source.events) != 3 {
		t.Fatal("failed export lost queue records")
	}
	source.failAck = false
	run := func() {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- service.run(ctx) }()
		defer func() { cancel(); <-done }()
		deadline := time.After(10 * time.Second)
		for {
			summary := service.Summary(map[string]bool{"allowed": true}, 30)
			if summary.State == "ready" {
				if summary.Totals.Workflows.Runs != 1 || summary.Totals.Workflows.Succeeded != 1 || summary.Totals.Jobs.Runs != 0 {
					t.Fatalf("duplicate or leaked records: %+v", summary.Totals)
				}
				if summary.Totals.Workflows.DurationSeconds != 60 {
					t.Fatal("wrong workflow duration")
				}
				return
			}
			select {
			case err := <-done:
				done <- err
				t.Fatalf("worker: %v", err)
			case <-deadline:
				t.Fatal("worker did not publish")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	run()
	files, err := filepath.Glob(filepath.Join(dir, "parquet", "*.parquet"))
	if err != nil || len(files) != 1 {
		t.Fatalf("retry duplicated parquet: %v %v", files, err)
	}
	if err = os.Remove(filepath.Join(dir, "history.duckdb")); err != nil {
		t.Fatal(err)
	}
	service = New(source, dir, nil)
	run() // Source queue is empty. Only Parquet can recover the history.
	if total := service.Summary(map[string]bool{"hidden": true}, 7).Totals.Jobs.Reused; total != 1 {
		t.Fatalf("missing recovered job: %d", total)
	}
	if total := service.Summary(map[string]bool{}, 7).Totals.Workflows.Runs; total != 0 {
		t.Fatal("empty project grant exposed analytics")
	}
}

func TestSummaryDoesNotWaitForExporter(t *testing.T) {
	s := New(&memorySource{}, t.TempDir(), nil)
	start := time.Now()
	for i := 0; i < 1000; i++ {
		s.Summary(map[string]bool{}, 90)
	}
	if time.Since(start) > time.Second {
		t.Fatal("summary is doing blocking work")
	}
}
