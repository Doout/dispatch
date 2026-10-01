// Package conformance supplies reusable behavioral checks for runtime drivers.
// A driver fixture must isolate its resources and report observed mutations.
package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/doout/dispatch/internal/runtimecontract"
)

type Fixture struct {
	Manifest      runtimecontract.Manifest
	Execute       func(context.Context, runtimecontract.Operation) error
	MutationCount func() int
	ResourceCount func() int
}

// Run checks capability rejection, cancellation before side effects and repeated
// declarative deployment/cleanup. Ownership and immutable-source tests use the
// driver's real resource fixture in addition to these portable cases.
func Run(t *testing.T, factory func(*testing.T) Fixture) {
	t.Helper()
	t.Run("versioned capabilities", func(t *testing.T) {
		f := factory(t)
		if f.Manifest.APIVersion != runtimecontract.APIVersion || f.Manifest.Driver == "" {
			t.Fatalf("invalid manifest: %#v", f.Manifest)
		}
		seen := map[runtimecontract.Operation]bool{}
		for _, c := range f.Manifest.Capabilities {
			if seen[c.Operation] {
				t.Fatalf("duplicate capability: %s", c.Operation)
			}
			seen[c.Operation] = true
			if !c.Supported && c.Reason == "" {
				t.Fatalf("missing unsupported reason for %s", c.Operation)
			}
		}
		for _, op := range runtimecontract.Operations() {
			if !seen[op] {
				t.Fatalf("missing capability decision for %s", op)
			}
		}
	})
	t.Run("unsupported operations do not mutate", func(t *testing.T) {
		f := factory(t)
		for _, c := range f.Manifest.Capabilities {
			if c.Supported {
				continue
			}
			before := f.MutationCount()
			err := f.Execute(context.Background(), c.Operation)
			var classified *runtimecontract.Error
			if !errors.As(err, &classified) || classified.Code != runtimecontract.Unsupported {
				t.Fatalf("%s must reject with unsupported_operation: %v", c.Operation, err)
			}
			if f.MutationCount() != before {
				t.Fatalf("unsupported %s changed runtime resources", c.Operation)
			}
		}
	})
	t.Run("cancelled requests do not mutate", func(t *testing.T) {
		f := factory(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for _, op := range []runtimecontract.Operation{runtimecontract.Deploy, runtimecontract.Destroy} {
			before := f.MutationCount()
			if err := f.Execute(ctx, op); !errors.Is(err, context.Canceled) {
				t.Fatalf("%s did not preserve cancellation: %v", op, err)
			}
			if f.MutationCount() != before {
				t.Fatalf("cancelled %s changed runtime resources", op)
			}
		}
	})
	t.Run("repeated deployment and cleanup", func(t *testing.T) {
		f := factory(t)
		ctx := context.Background()
		if f.Manifest.Check(ctx, runtimecontract.Deploy) != nil || f.Manifest.Check(ctx, runtimecontract.Destroy) != nil {
			t.Skip("driver does not advertise both deploy and destroy")
		}
		for range 2 {
			if err := f.Execute(ctx, runtimecontract.Deploy); err != nil {
				t.Fatal(err)
			}
			if count := f.ResourceCount(); count != 1 {
				t.Fatalf("repeated deployment must leave one owned workload, got %d", count)
			}
		}
		for range 2 {
			if err := f.Execute(ctx, runtimecontract.Destroy); err != nil {
				t.Fatal(err)
			}
			if count := f.ResourceCount(); count != 0 {
				t.Fatalf("cleanup left %d workloads", count)
			}
		}
	})
}
