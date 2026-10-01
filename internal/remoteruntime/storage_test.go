package remoteruntime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func retainedVolume(t *testing.T, b *Broker, r Request) core.StorageResource {
	t.Helper()
	data := b.Store.(*store.SQLStore)
	item := core.StorageResource{ID: fmt.Sprintf("storage-%x", sha256.Sum256([]byte(r.Server.ID+"\x00docker_volume\x00\x00database"))), ServerID: r.Server.ID, Kind: "docker_volume", Name: "database", Identity: "created:local", Evidence: "owned-labels", ProjectID: r.Application.ProjectID, OwnerKind: "application", OwnerID: r.Application.ID, Ownership: "verified", State: "present", Consumers: []core.StorageConsumer{}}
	if err := data.ObserveStorage(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	item, _ = data.GetStorage(context.Background(), item.ID)
	if err := data.SetStoragePolicy(context.Background(), item.ID, item.Revision, "destroy"); err != nil {
		t.Fatal(err)
	}
	item, _ = data.GetStorage(context.Background(), item.ID)
	return item
}

func TestRemoteStorageUsesTargetAndRetainedOwnership(t *testing.T) {
	b, r := brokerFixture(t)
	ctx := context.Background()
	item := retainedVolume(t, b, r)
	if err := b.Store.(*store.SQLStore).DeleteApp(ctx, r.Application.ID); err != nil {
		t.Fatal(err)
	}
	request := NewStorageRequest(r.Server, &item)
	job, err := b.Submit(ctx, "delete-orphan", request)
	if err != nil {
		t.Fatal(err)
	}
	if lease, err := b.Lease(ctx, "wrong-node"); err != nil || lease != nil {
		t.Fatalf("wrong target leased: %v %v", lease, err)
	}
	leased, err := b.Lease(ctx, r.Server.AgentNodeID)
	if err != nil || leased == nil || leased.Request.Storage.ID != item.ID {
		t.Fatalf("retained owner lease: %v %v", leased, err)
	}
	if err = b.Complete(ctx, r.Server.AgentNodeID, job.ID, Completion{LeaseToken: leased.LeaseToken, Result: Result{State: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Wait(ctx, job.ID, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteStorageRejectsRetainedChangedAndUnverifiedDeletion(t *testing.T) {
	for _, kind := range []string{"retained", "unverified", "consumer", "changed-after-submit", "wrong-target", "wrong-name"} {
		t.Run(kind, func(t *testing.T) {
			b, r := brokerFixture(t)
			ctx := context.Background()
			item := retainedVolume(t, b, r)
			switch kind {
			case "retained":
				item.Policy = "retain"
			case "unverified":
				item.Ownership = "unverified"
			case "consumer":
				item.Consumers = []core.StorageConsumer{{ID: "stopped-container"}}
			case "wrong-target":
				item.ServerID = "other-target"
			case "wrong-name":
				item.Name = "replacement"
			}
			_, err := b.Submit(ctx, "delete", NewStorageRequest(r.Server, &item))
			if kind != "changed-after-submit" {
				if err == nil {
					t.Fatal("unsafe deletion accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = b.Store.(*store.SQLStore).SetStoragePolicy(ctx, item.ID, item.Revision, "retain"); err != nil {
				t.Fatal(err)
			}
			if lease, err := b.Lease(ctx, r.Server.AgentNodeID); err == nil || lease != nil {
				t.Fatalf("stale deletion review leased: %v %v", lease, err)
			}
		})
	}
}

func TestRemoteStorageRequiresCompleteBoundedInventory(t *testing.T) {
	b, r := brokerFixture(t)
	ctx := context.Background()
	request := NewStorageRequest(r.Server, nil)
	job, err := b.Submit(ctx, "inventory", request)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := b.Lease(ctx, r.Server.AgentNodeID)
	if err != nil || lease == nil {
		t.Fatal(err)
	}
	if err = b.Complete(ctx, r.Server.AgentNodeID, job.ID, Completion{LeaseToken: lease.LeaseToken, Result: Result{State: "succeeded"}}); err == nil {
		t.Fatal("missing evidence declared target empty")
	}
	bad := Result{State: "succeeded", Storage: []core.StorageObservation{{Resource: core.StorageResource{Kind: "docker_volume", Name: "data", Identity: "id", Evidence: "labels", Independent: true}}}}
	if err = b.Complete(ctx, r.Server.AgentNodeID, job.ID, Completion{LeaseToken: lease.LeaseToken, Result: bad}); err == nil {
		t.Fatal("local volume accepted as independently preserved")
	}
	if err = b.Complete(ctx, r.Server.AgentNodeID, job.ID, Completion{LeaseToken: lease.LeaseToken, Result: Result{State: "succeeded", Storage: []core.StorageObservation{}}}); err != nil {
		t.Fatal(err)
	}
	result, err := b.Wait(ctx, job.ID, nil)
	if err != nil || result.Storage == nil {
		t.Fatalf("complete empty evidence lost: %#v %v", result, err)
	}
}

func TestRemoteStorageUnknownDeletionRequiresInventoryAfterLease(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%t", expired), func(t *testing.T) {
			b, r := brokerFixture(t)
			ctx := context.Background()
			item := retainedVolume(t, b, r)
			request := NewStorageRequest(r.Server, &item)
			job, err := b.Submit(ctx, "delete", request)
			if err != nil {
				t.Fatal(err)
			}
			when := time.Now().UTC()
			if expired {
				when = when.Add(-2 * time.Minute)
			}
			lease, err := b.Store.LeaseRuntimeJob(ctx, r.Server.AgentNodeID, when, LeaseDuration)
			if err != nil || lease == nil {
				t.Fatal(err)
			}
			if err = b.Store.CompleteRuntimeJob(ctx, r.Server.AgentNodeID, job.ID, lease.LeaseToken, "unknown", "", when.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err = b.Submit(ctx, "repeat", request); !errors.Is(err, store.ErrRuntimeJobConflict) {
				t.Fatalf("uninspected deletion repeated: %v", err)
			}
			inspect, err := b.Submit(ctx, "inspect", NewStorageRequest(r.Server, nil))
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := b.Lease(ctx, r.Server.AgentNodeID)
			if err != nil || inspection == nil {
				t.Fatal(err)
			}
			if err = b.Complete(ctx, r.Server.AgentNodeID, inspect.ID, Completion{LeaseToken: inspection.LeaseToken, Result: Result{State: "succeeded", Storage: []core.StorageObservation{}}}); err != nil {
				t.Fatal(err)
			}
			if err = b.Store.ReconcileStorageRuntimeJobs(ctx, r.Server.ID, inspect.ID, time.Now().Add(2*time.Minute)); err != nil {
				t.Fatal(err)
			}
			_, err = b.Submit(ctx, "repeat", request)
			if expired && err != nil {
				t.Fatalf("freshly inspected deletion remained blocked: %v", err)
			}
			if !expired && !errors.Is(err, store.ErrRuntimeJobConflict) {
				t.Fatalf("inventory captured during prior lease unlocked deletion: %v", err)
			}
		})
	}
}
