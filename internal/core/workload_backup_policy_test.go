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

func TestOffsiteRecoveryPointFreshnessDoesNotRenewWithVerification(t *testing.T) {
	now := time.Now().UTC()
	captured := now.Add(-3 * time.Hour)
	justVerified := now
	p := WorkloadBackupPolicy{OffsiteStoreID: "approved-store", OffsiteStaleAfterHours: 2, LastOffsiteRecoveryPointAt: &captured, LastOffsiteVerifiedAt: &justVerified, CreatedAt: captured}
	if p.OffsiteFreshnessAt(now) != "stale" {
		t.Fatal("verifying old captured data falsely refreshed protection")
	}
	p.LastOffsiteRecoveryPointAt = nil
	if p.OffsiteFreshnessAt(now) != "stale" {
		t.Fatal("no confirmed recovery point remained fresh beyond its initial window")
	}
	p.CreatedAt = now
	if p.OffsiteFreshnessAt(now) != "not_protected" {
		t.Fatal("first capture was reported as confirmed protection")
	}
	p.OffsiteStoreID = ""
	if p.OffsiteFreshnessAt(now) != "disabled" {
		t.Fatal("target-local policy claimed offsite protection")
	}
}
