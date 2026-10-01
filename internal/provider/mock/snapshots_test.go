package mock_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
)

func finish(t *testing.T, p provider.Provider, op provider.Operation) provider.Operation {
	t.Helper()
	for i := 0; i < 5 && !provider.Terminal(op); i++ {
		var err error
		op, err = p.Operation(context.Background(), op.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !provider.Terminal(op) {
		t.Fatal("operation did not terminate")
	}
	return op
}
func captureInput(source provider.Server) provider.CreateSnapshotRequest {
	in := provider.CreateSnapshotRequest{Name: "retained fixture", SourceServerID: source.ID, DiskSet: "all", Consistency: provider.ConsistencyCrash, Encryption: provider.SnapshotEncryption{Mode: "provider-managed"}, Labels: map[string]string{"dispatch.project": "fixture"}}
	for _, d := range source.Disks {
		in.DiskIDs = append(in.DiskIDs, d.ID)
	}
	return in
}
func readySource(t *testing.T, p provider.Provider) provider.Server {
	t.Helper()
	op, err := p.CreateServer(context.Background(), "source", request())
	if err != nil {
		t.Fatal(err)
	}
	op = finish(t, p, op)
	source, err := p.Server(context.Background(), op.ResourceID)
	if err != nil || source.State != "ready" {
		t.Fatal(source, err)
	}
	return source
}
func restoreInput(snapshot provider.Snapshot) provider.RestoreServerRequest {
	in := request()
	in.Name = "isolated clone"
	in.Network = "mock-isolated"
	return provider.RestoreServerRequest{Server: in, Policy: provider.IsolatedRestorePolicy(), ExpectedSnapshotDigest: provider.SnapshotDigest(snapshot)}
}
func expectStatus(t *testing.T, err error, status int) {
	t.Helper()
	var problem *provider.Problem
	if !errors.As(err, &problem) || problem.Status != status {
		t.Fatalf("expected status %d: %v", status, err)
	}
}

func TestSnapshotRestartConcurrentReplayAndIndependentResources(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	start := func() (*mock.Mock, *provider.Client, func()) {
		t.Helper()
		a, err := mock.New(mock.Options{StateFile: path, Polls: 2})
		if err != nil {
			t.Fatal(err)
		}
		s := httptest.NewServer(provider.Handler(a, "fixture-token"))
		c, err := provider.NewClient(s.URL, "fixture-token", s.Client())
		if err != nil {
			t.Fatal(err)
		}
		return a, c, s.Close
	}
	_, client, closeServer := start()
	source := readySource(t, client)
	in := captureInput(source)
	accepted, err := client.CreateSnapshot(ctx, "capture", in)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			again, err := client.CreateSnapshot(ctx, "capture", in)
			if err == nil && (again.ID != accepted.ID || again.ResourceID != accepted.ResourceID) {
				err = errors.New("replay changed resource")
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	closeServer()
	_, client, closeServer = start()
	defer func() { closeServer() }()
	again, err := client.CreateSnapshot(ctx, "capture", in)
	if err != nil || again != accepted {
		t.Fatal(again, err)
	}
	finish(t, client, again)
	snapshot, err := client.Snapshot(ctx, accepted.ResourceID)
	if err != nil || snapshot.State != "ready" {
		t.Fatal(snapshot, err)
	}
	listed, err := client.Snapshots(ctx, source.ID)
	if err != nil || len(listed) != 1 {
		t.Fatal(listed, err)
	}
	bad := in
	bad.Name = "changed"
	_, err = client.CreateSnapshot(ctx, "capture", bad)
	expectStatus(t, err, 409)
	_, err = client.DeleteServer(ctx, "capture", source.ID)
	expectStatus(t, err, 409)
	restore := restoreInput(snapshot)
	cloneOp, err := client.RestoreServer(ctx, "restore", snapshot.ID, restore)
	if err != nil {
		t.Fatal(err)
	}
	closeServer()
	_, client, closeServer = start()
	repeated, err := client.RestoreServer(ctx, "restore", snapshot.ID, restore)
	if err != nil || repeated != cloneOp {
		t.Fatal(repeated, err)
	}
	finish(t, client, cloneOp)
	clone, err := client.Server(ctx, cloneOp.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if err = provider.VerifyCloneEvidence(snapshot, clone); err != nil {
		t.Fatal(err)
	}
	original, err := client.Server(ctx, source.ID)
	if err != nil || !reflect.DeepEqual(source, original) {
		t.Fatal("restore changed source", err)
	}
	// Removing either machine cannot remove retained snapshot data.
	for i, id := range []string{source.ID, clone.ID} {
		key := []string{"delete-source", "delete-clone"}[i]
		op, err := client.DeleteServer(ctx, key, id)
		if err != nil {
			t.Fatal(err)
		}
		finish(t, client, op)
		if _, err = client.Snapshot(ctx, snapshot.ID); err != nil {
			t.Fatal("machine deletion lost retained snapshot", err)
		}
	}
	op, err := client.DeleteSnapshot(ctx, "delete-snapshot", snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	finish(t, client, op)
	_, err = client.Snapshot(ctx, snapshot.ID)
	expectStatus(t, err, 404)
	repeated, err = client.DeleteSnapshot(ctx, "delete-snapshot", snapshot.ID)
	if err != nil || repeated.ID != op.ID {
		t.Fatal(repeated, err)
	}
	listed, err = client.Snapshots(ctx, source.ID)
	if err != nil || len(listed) != 0 {
		t.Fatal(listed, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state struct{ Version int }
	if json.Unmarshal(raw, &state) != nil || state.Version != 2 {
		t.Fatal("snapshot state did not persist its version")
	}
}

func TestSnapshotPoliciesAndUnsafeRestoreEvidence(t *testing.T) {
	for _, test := range []struct {
		name    string
		options mock.Options
	}{
		{"normal", mock.Options{}}, {"capture-failure", mock.Options{FailSnapshot: true}}, {"corrupt", mock.Options{CorruptSnapshot: true}}, {"restore-failure", mock.Options{FailRestore: true}}, {"unsafe-clone", mock.Options{UnsafeRestore: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			test.options.Polls = 1
			a, err := mock.New(test.options)
			if err != nil {
				t.Fatal(err)
			}
			source := readySource(t, a)
			in := captureInput(source)
			unsupported := in
			unsupported.Consistency = provider.ConsistencyApplication
			_, err = a.CreateSnapshot(ctx, "unsupported", unsupported)
			expectStatus(t, err, 422)
			unsupported = in
			unsupported.DiskIDs = []string{source.Disks[0].ID, source.Disks[0].ID}
			_, err = a.CreateSnapshot(ctx, "bad-disks", unsupported)
			expectStatus(t, err, 422)
			items, _ := a.Snapshots(ctx, source.ID)
			if len(items) != 0 {
				t.Fatal("rejected policy allocated snapshot")
			}
			op, err := a.CreateSnapshot(ctx, "capture", in)
			if err != nil {
				t.Fatal(err)
			}
			op = finish(t, a, op)
			snapshot, err := a.Snapshot(ctx, op.ResourceID)
			if err != nil {
				t.Fatal(err)
			}
			restore := restoreInput(snapshot)
			if test.options.FailSnapshot || test.options.CorruptSnapshot {
				_, err = a.RestoreServer(ctx, "restore", snapshot.ID, restore)
				expectStatus(t, err, 409)
				return
			}
			unsafe := restore
			unsafe.Policy.AllowProductionBindings = true
			_, err = a.RestoreServer(ctx, "unsafe-policy", snapshot.ID, unsafe)
			expectStatus(t, err, 422)
			incompatible := restore
			incompatible.Server.Image = "different-linux"
			_, err = a.RestoreServer(ctx, "wrong-image", snapshot.ID, incompatible)
			expectStatus(t, err, 422)
			stale := restore
			stale.ExpectedSnapshotDigest = "stale"
			_, err = a.RestoreServer(ctx, "stale", snapshot.ID, stale)
			expectStatus(t, err, 409)
			_, err = a.RestoreServer(ctx, "missing", "missing-snapshot", restore)
			expectStatus(t, err, 404)
			op, err = a.RestoreServer(ctx, "restore", snapshot.ID, restore)
			if err != nil {
				t.Fatal(err)
			}
			op = finish(t, a, op)
			clone, err := a.Server(ctx, op.ResourceID)
			if err != nil {
				t.Fatal(err)
			}
			if test.options.FailRestore {
				if op.State != provider.StateFailed {
					t.Fatal("restore failure missing")
				}
				return
			}
			err = provider.VerifyCloneEvidence(snapshot, clone)
			if test.options.UnsafeRestore {
				if err == nil {
					t.Fatal("copied identity and journal accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			clone.Disks[1].ID = clone.Disks[0].ID
			if provider.VerifyCloneEvidence(snapshot, clone) == nil {
				t.Fatal("aliased clone disks accepted")
			}
			clone, _ = a.Server(ctx, op.ResourceID)
			clone.Restore.RuntimeJournalCleared = false
			if provider.VerifyCloneEvidence(snapshot, clone) == nil {
				t.Fatal("copied runtime journal accepted")
			}
			clone, _ = a.Server(ctx, op.ResourceID)
			clone.Disks[0].ContentDigest = "corrupted"
			if provider.VerifyCloneEvidence(snapshot, clone) == nil {
				t.Fatal("disk integrity mismatch accepted")
			}
			deleteOp, err := a.DeleteSnapshot(ctx, "delete-snapshot", snapshot.ID)
			if err != nil {
				t.Fatal(err)
			}
			finish(t, a, deleteOp)
			if _, err = a.Server(ctx, op.ResourceID); err != nil {
				t.Fatal("snapshot deletion removed clone", err)
			}
		})
	}
}

func TestSnapshotsOptionalAndLegacyStateUpgrade(t *testing.T) {
	ctx := context.Background()
	a, _ := mock.New(mock.Options{DisableSnapshots: true})
	manifest, _ := a.Manifest(ctx)
	if manifest.Snapshots != nil || provider.ValidateManifest(manifest) != nil {
		t.Fatal("invalid server-only provider")
	}
	_, err := a.CreateSnapshot(ctx, "capture", provider.CreateSnapshotRequest{})
	expectStatus(t, err, 422)
	path := filepath.Join(t.TempDir(), "legacy.json")
	legacy := []byte(`{"version":1,"servers":{"legacy":{"id":"legacy","name":"old","state":"ready"}},"operations":{},"keys":{}}`)
	if err = os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	a, err = mock.New(mock.Options{StateFile: path, Polls: 1})
	if err != nil {
		t.Fatal(err)
	}
	source, err := a.Server(ctx, "legacy")
	if err != nil || len(source.Disks) != 2 {
		t.Fatal(source, err)
	}
	if _, err = a.CreateSnapshot(ctx, "capture", captureInput(source)); err != nil {
		t.Fatal(err)
	}
	a, err = mock.New(mock.Options{StateFile: path, Polls: 1})
	if err != nil {
		t.Fatal(err)
	}
	items, _ := a.Snapshots(ctx, "legacy")
	if len(items) != 1 {
		t.Fatal("upgraded state lost snapshot")
	}
}

func TestSnapshotHTTPRejectsForeignEvidence(t *testing.T) {
	ctx := context.Background()
	a, _ := mock.New(mock.Options{Polls: 1})
	source := readySource(t, a)
	op, _ := a.CreateSnapshot(ctx, "capture", captureInput(source))
	finish(t, a, op)
	snapshot, _ := a.Snapshot(ctx, op.ResourceID)
	for _, list := range []bool{false, true} {
		t.Run(map[bool]string{false: "inspect", true: "list"}[list], func(t *testing.T) {
			snapshot.SourceServerID = "foreign-source"
			snapshot.ID = "foreign-snapshot"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if list {
					_ = json.NewEncoder(w).Encode(map[string]any{"items": []provider.Snapshot{snapshot}})
				} else {
					_ = json.NewEncoder(w).Encode(snapshot)
				}
			}))
			defer server.Close()
			c, _ := provider.NewClient(server.URL, "", server.Client())
			var err error
			if list {
				_, err = c.Snapshots(ctx, source.ID)
			} else {
				_, err = c.Snapshot(ctx, op.ResourceID)
			}
			if err == nil {
				t.Fatal("foreign snapshot evidence accepted")
			}
		})
	}
}
