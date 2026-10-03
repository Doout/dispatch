package core

import (
	"testing"
	"time"
)

func TestBackupCaptureCadenceCoalescesDowntime(t *testing.T) {
	initial := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	p := WorkloadBackupPolicy{IntervalHours: 2, NextCaptureAt: initial}
	slot, next, missed := p.DueCapture(initial.Add(7 * time.Hour))
	if !slot.Equal(initial.Add(6*time.Hour)) || !next.Equal(initial.Add(8*time.Hour)) || missed != 3 {
		t.Fatal(slot, next, missed)
	}
	slot, next, missed = p.DueCapture(initial.Add(-time.Second))
	if !slot.IsZero() || !next.Equal(initial) || missed != 0 {
		t.Fatal("early capture changed deadline")
	}
}
