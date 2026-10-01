package mock_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
)

func TestSnapshotConformanceRejectsFaultsAndChecksCleanup(t *testing.T) {
	for _, test := range []struct {
		name    string
		options mock.Options
		success bool
	}{
		{"safe", mock.Options{}, true}, {"unsupported", mock.Options{DisableSnapshots: true}, false}, {"corrupt", mock.Options{CorruptSnapshot: true}, false}, {"capture-failed", mock.Options{FailSnapshot: true}, false}, {"restore-failed", mock.Options{FailRestore: true}, false}, {"unsafe-identities", mock.Options{UnsafeRestore: true}, false}, {"delete-failed", mock.Options{FailDelete: true}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.options.Polls = 1
			a, err := mock.New(test.options)
			if err != nil {
				t.Fatal(err)
			}
			s := httptest.NewServer(provider.Handler(a, ""))
			defer s.Close()
			c, _ := provider.NewClient(s.URL, "", s.Client())
			report, err := provider.RunSnapshotConformance(context.Background(), c, provider.ConformanceOptions{Request: request(), Timeout: 2 * time.Second, PollInterval: time.Millisecond})
			if (err == nil) != test.success {
				t.Fatalf("report=%+v error=%v", report, err)
			}
			if test.success && len(report.Checks) != 6 {
				t.Fatal(report)
			}
			if !test.success && !test.options.DisableSnapshots {
				last := report.Checks[len(report.Checks)-1]
				if last.Name != "snapshot failure cleanup" || last.Passed == test.options.FailDelete {
					t.Fatalf("cleanup reported incorrectly: %+v", last)
				}
			}
		})
	}
}

func TestSnapshotManifestRequiresIsolationAndBoundedPolicies(t *testing.T) {
	a, _ := mock.New(mock.Options{})
	m, _ := a.Manifest(context.Background())
	if err := provider.ValidateManifest(m); err != nil {
		t.Fatal(err)
	}
	m.Snapshots.Restore.ClearRuntimeJournal = false
	if provider.ValidateManifest(m) == nil {
		t.Fatal("restore without journal sanitation advertised")
	}
	m.Snapshots.Restore.ClearRuntimeJournal = true
	m.Snapshots.Consistency = append(m.Snapshots.Consistency, m.Snapshots.Consistency[0])
	if provider.ValidateManifest(m) == nil {
		t.Fatal("duplicate snapshot policy advertised")
	}
}
