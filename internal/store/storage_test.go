package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestStorageInventoryPersistenceAndProtection(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			ctx := context.Background()
			dsn := filepath.Join(t.TempDir(), "storage.db")
			if dialect == "postgres" {
				dsn = os.Getenv("DISPATCH_TEST_POSTGRES_URL")
				if dsn == "" {
					t.Skip("set DISPATCH_TEST_POSTGRES_URL to exercise PostgreSQL storage")
				}
			}
			data, err := Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer data.Close()
			if err = data.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			id := ulid.Make().String()
			server := core.Server{ID: id, Name: id, Address: "example.test", Runtime: core.ServerRuntimeDocker, State: "ready", CreatedAt: time.Now().UTC()}
			if err = data.CreateServer(ctx, server); err != nil {
				t.Fatal(err)
			}
			item := core.StorageResource{ID: id, ServerID: id, Kind: "provider_disk", Name: "disk", Identity: "disk-v1", ProjectID: "project", OwnerKind: "application", OwnerID: "app", Ownership: "verified", State: "present", Consumers: []core.StorageConsumer{}, ObservedAt: time.Now().UTC()}
			if err = data.ObserveStorage(ctx, item); err != nil {
				t.Fatal(err)
			}
			got, err := data.GetStorage(ctx, id)
			if err != nil || got.Policy != "retain" || got.Revision != 1 {
				t.Fatalf("initial %+v %v", got, err)
			}
			if err = data.DeleteServer(ctx, id); !errors.Is(err, ErrStorageProtected) {
				t.Fatalf("protected target deletion: %v", err)
			}
			if err = data.SetStoragePolicy(ctx, id, got.Revision, "destroy"); err != nil {
				t.Fatal(err)
			}
			if err = data.SetStoragePolicy(ctx, id, got.Revision, "retain"); !errors.Is(err, ErrStorageChanged) {
				t.Fatalf("stale policy: %v", err)
			}
			got, _ = data.GetStorage(ctx, id)
			revision := got.Revision
			got.ObservedAt = time.Now().UTC().Add(time.Minute)
			if err = data.ObserveStorage(ctx, got); err != nil {
				t.Fatal(err)
			}
			got, _ = data.GetStorage(ctx, id)
			if got.Revision != revision || got.Policy != "destroy" {
				t.Fatal("freshness changed review or lost policy")
			}
			got.Consumers = []core.StorageConsumer{{ID: "other-workload", Mount: "/data", Active: true}}
			if err = data.ObserveStorage(ctx, got); err != nil {
				t.Fatal(err)
			}
			got, _ = data.GetStorage(ctx, id)
			if got.Revision <= revision || got.DeleteBlockedReason() == "" {
				t.Fatal("new consumer did not protect storage")
			}
			got.Identity = "replacement-disk"
			got.Consumers = []core.StorageConsumer{}
			if err = data.ObserveStorage(ctx, got); err != nil {
				t.Fatal(err)
			}
			got, _ = data.GetStorage(ctx, id)
			if got.Policy != "retain" {
				t.Fatal("replacement inherited destructive policy")
			}
			got.State = "absent"
			if err = data.ObserveStorage(ctx, got); err != nil {
				t.Fatal(err)
			}
			if err = data.DeleteServer(ctx, id); err != nil {
				t.Fatal(err)
			}
			if _, err = data.GetStorage(ctx, id); err != nil {
				t.Fatal("target removal discarded storage history", err)
			}
			if err = data.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if err = data.Close(); err != nil {
				t.Fatal(err)
			}
			data, err = Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer data.Close()
			got, err = data.GetStorage(ctx, id)
			if err != nil || got.Identity != "replacement-disk" || got.State != "absent" {
				t.Fatalf("restart: %+v %v", got, err)
			}
		})
	}
}
