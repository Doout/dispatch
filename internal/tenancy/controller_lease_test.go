package tenancy

import (
	"context"
	"testing"
)

func TestPostgresControllerLeaseRejectsConcurrentController(t *testing.T) {
	catalog := testCatalog(t, true)
	release, err := catalog.AcquireControllerLease(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if other, err := catalog.AcquireControllerLease(context.Background(), nil); err == nil {
		other()
		t.Fatal("second controller acquired the same catalog")
	}
	release()
	next, err := catalog.AcquireControllerLease(context.Background(), nil)
	if err != nil {
		t.Fatal("released lease cannot be acquired", err)
	}
	next()
}
