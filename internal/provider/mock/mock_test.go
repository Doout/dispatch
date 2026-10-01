package mock_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
)

func request() provider.CreateServerRequest {
	return provider.CreateServerRequest{Name: "Mock fixture", Region: "mock-region", Size: "mock-small", Image: "mock-linux", Network: "mock-private", SSHKey: "public-mock-key", Bootstrap: "one-use-fixture-must-not-be-persisted", ProviderConfig: map[string]any{}}
}

func TestRestartReconcilesCreateDeleteAndIdempotency(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	start := func() *mock.Mock {
		t.Helper()
		adapter, err := mock.New(mock.Options{StateFile: path, Polls: 2})
		if err != nil {
			t.Fatal(err)
		}
		return adapter
	}
	adapter := start()
	first, err := adapter.CreateServer(ctx, "create", request())
	if err != nil {
		t.Fatal(err)
	}
	adapter = start()
	again, err := adapter.CreateServer(ctx, "create", request())
	if err != nil || first != again {
		t.Fatalf("restart changed create: %+v %+v %v", first, again, err)
	}
	running, err := adapter.Operation(ctx, first.ID)
	if err != nil || running.State != provider.StateRunning {
		t.Fatalf("create did not run: %+v %v", running, err)
	}
	adapter = start()
	done, err := adapter.Operation(ctx, first.ID)
	if err != nil || done.State != provider.StateSucceeded || done.ResourceID != first.ResourceID {
		t.Fatalf("create did not recover: %+v %v", done, err)
	}
	deletion, err := adapter.DeleteServer(ctx, "delete", done.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	adapter = start()
	repeat, err := adapter.DeleteServer(ctx, "delete", done.ResourceID)
	if err != nil || repeat != deletion {
		t.Fatalf("restart changed delete: %+v %v", repeat, err)
	}
	_, _ = adapter.Operation(ctx, deletion.ID)
	adapter = start()
	removed, err := adapter.Operation(ctx, deletion.ID)
	if err != nil || removed.State != provider.StateSucceeded {
		t.Fatalf("delete did not recover: %+v %v", removed, err)
	}
	_, err = adapter.Server(ctx, done.ResourceID)
	var problem *provider.Problem
	if !errors.As(err, &problem) || problem.Status != 404 {
		t.Fatal("deleted resource remained")
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), request().Bootstrap) || strings.Contains(string(raw), request().SSHKey) {
		t.Fatal("mock state retained bootstrap or SSH inputs")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("state file is not private")
	}
}

func TestMockFailureModesAndConflictingMutationKeys(t *testing.T) {
	ctx := context.Background()
	for _, deletion := range []bool{false, true} {
		adapter, err := mock.New(mock.Options{Polls: 1, FailCreate: !deletion, FailDelete: deletion})
		if err != nil {
			t.Fatal(err)
		}
		op, err := adapter.CreateServer(ctx, "create", request())
		if err != nil {
			t.Fatal(err)
		}
		changed := request()
		changed.Name = "" // Even invalid changed inputs must preserve the original key binding.
		_, err = adapter.CreateServer(ctx, "create", changed)
		var problem *provider.Problem
		if !errors.As(err, &problem) || problem.Status != 409 {
			t.Fatal("changed create inputs reused an idempotency key")
		}
		op, err = adapter.Operation(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if deletion {
			op, err = adapter.DeleteServer(ctx, "delete", op.ResourceID)
			if err != nil {
				t.Fatal(err)
			}
			op, err = adapter.Operation(ctx, op.ID)
		}
		if err != nil || op.State != provider.StateFailed {
			t.Fatalf("failure mode missing: %+v %v", op, err)
		}
		if _, err := adapter.Server(ctx, op.ResourceID); err != nil {
			t.Fatal("failed operation lost inspectable resource")
		}
		terminal, err := adapter.Operation(ctx, op.ID)
		if err != nil || terminal != op {
			t.Fatal("terminal failure was not stable")
		}
	}
}

func TestMockRejectsDeletionDuringCreateAndUnsafeStateFiles(t *testing.T) {
	ctx := context.Background()
	adapter, _ := mock.New(mock.Options{})
	op, err := adapter.CreateServer(ctx, "create", request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.DeleteServer(ctx, "delete", op.ResourceID); err == nil {
		t.Fatal("deletion raced unfinished creation")
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err = os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = mock.New(mock.Options{StateFile: path}); err == nil {
		t.Fatal("public state file accepted")
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = mock.New(mock.Options{StateFile: path}); err == nil {
		t.Fatal("invalid state file accepted")
	}
}
