package deploy

import (
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	helmrelease "helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
)

func TestDriftReleaseRequiresUniqueCompletedSnapshotMatch(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	finished := start.Add(time.Minute)
	d := core.Deployment{CreatedAt: start, FinishedAt: &finished, Snapshot: core.DeploymentSnapshot{TargetID: "target", Values: map[string]any{"replicas": float64(2)}}}
	original := &helmrelease.Release{Name: "app", Namespace: "ns", Version: 3, Manifest: "original", Config: map[string]any{"replicas": float64(2)}, Info: &helmrelease.Info{Status: helmrelease.StatusSuperseded, LastDeployed: helmtime.Time{Time: start.Add(time.Second)}}}
	later := &helmrelease.Release{Name: "app", Namespace: "ns", Version: 4, Manifest: "changed", Config: map[string]any{"replicas": float64(3)}, Info: &helmrelease.Info{Status: helmrelease.StatusDeployed, LastDeployed: helmtime.Time{Time: finished.Add(time.Hour)}}}
	selected, err := selectDriftRelease([]*helmrelease.Release{later, original}, "ns", "app", d)
	if err != nil || !selected.Matched || selected.Manifest != "original" {
		t.Fatal(selected, err)
	}
	// A current release alone cannot establish the old desired configuration.
	selected, err = selectDriftRelease([]*helmrelease.Release{later}, "ns", "app", d)
	if err != nil || selected.Matched || selected.Manifest != "changed" {
		t.Fatal(selected, err)
	}
	duplicate := *original
	duplicate.Version = 2
	selected, err = selectDriftRelease([]*helmrelease.Release{later, original, &duplicate}, "ns", "app", d)
	if err != nil || selected.Matched {
		t.Fatal("ambiguous release was adopted", selected, err)
	}
	d.FinishedAt = nil
	selected, err = selectDriftRelease([]*helmrelease.Release{original}, "ns", "app", d)
	if err != nil || selected.Matched {
		t.Fatal("unbounded deployment was adopted")
	}
	if _, err = selectDriftRelease([]*helmrelease.Release{original}, "other", "app", d); err == nil {
		t.Fatal("cross-namespace release accepted")
	}
}
