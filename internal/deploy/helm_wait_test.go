package deploy

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/resource"
)

type helmReadinessFunc func(context.Context, *resource.Info) (bool, error)

func (f helmReadinessFunc) IsReady(ctx context.Context, info *resource.Info) (bool, error) {
	return f(ctx, info)
}

func TestHelmCandidateWaitStopsBeforeAtomicRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, stopped := make(chan struct{}), make(chan struct{})
	var checks, recoveries atomic.Int32
	w := &helmActionWaiter{ctx: ctx, interval: time.Millisecond}
	w.checker = func(jobs bool) helmReadinessChecker {
		if !jobs {
			t.Error("candidate job readiness was omitted")
		}
		return helmReadinessFunc(func(ctx context.Context, _ *resource.Info) (bool, error) {
			checks.Add(1)
			close(entered)
			defer close(stopped)
			<-ctx.Done()
			return false, ctx.Err()
		})
	}
	resources := kube.ResourceList{&resource.Info{Name: "candidate"}}
	timeout := time.Minute
	w.recoveryWait = func(got kube.ResourceList, bound time.Duration, jobs bool) error {
		select {
		case <-stopped:
		default:
			t.Error("recovery overlapped candidate readiness")
		}
		if len(got) != 1 || got[0].Name != "previous" || bound != timeout || !jobs {
			t.Error("recovery lost its resources, timeout or job checks")
		}
		recoveries.Add(1)
		return nil
	}
	result := make(chan error, 1)
	go func() { result <- w.WaitWithJobs(resources, timeout) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation result: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("candidate readiness did not stop")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("candidate waiter survived its result")
	}
	if err := w.WaitWithJobs(kube.ResourceList{&resource.Info{Name: "previous"}}, timeout); err != nil {
		t.Fatalf("bounded recovery: %v", err)
	}
	if checks.Load() != 1 || recoveries.Load() != 1 {
		t.Fatalf("candidate=%d recovery=%d", checks.Load(), recoveries.Load())
	}
}

func TestHelmCandidateWaitHonorsTimeout(t *testing.T) {
	w := &helmActionWaiter{ctx: context.Background(), interval: time.Millisecond}
	var checks int
	w.checker = func(bool) helmReadinessChecker {
		return helmReadinessFunc(func(context.Context, *resource.Info) (bool, error) { checks++; return false, nil })
	}
	err := w.Wait(kube.ResourceList{&resource.Info{Name: "unready"}}, 25*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || checks == 0 {
		t.Fatalf("checks=%d error=%v", checks, err)
	}
}

func TestHelmCandidateWaitRetriesAvailabilityButRejectsMissingResources(t *testing.T) {
	for _, scenario := range []struct {
		name                 string
		failure              error
		failCount, wantCalls int
		succeeds             bool
	}{
		{"transient", apierrors.NewServiceUnavailable("unavailable"), 2, 3, true},
		{"missing", apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "candidate"), 1, 1, false},
		{"bounded-retries", &apierrors.StatusError{ErrStatus: metav1.Status{Code: 503}}, 40, 31, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			w := &helmActionWaiter{ctx: context.Background(), interval: time.Millisecond}
			w.checker = func(bool) helmReadinessChecker {
				return helmReadinessFunc(func(context.Context, *resource.Info) (bool, error) {
					calls++
					if calls <= scenario.failCount {
						return false, scenario.failure
					}
					return true, nil
				})
			}
			err := w.Wait(kube.ResourceList{&resource.Info{Name: "candidate"}}, time.Second)
			if (err == nil) != scenario.succeeds || calls != scenario.wantCalls {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}
