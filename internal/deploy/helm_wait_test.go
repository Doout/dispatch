package deploy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/resource"
	restfake "k8s.io/client-go/rest/fake"
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

func TestHelmApplyFailureAllowsRecoveryBeforeCandidateWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &helmActionWaiter{ctx: ctx, interval: time.Millisecond}
	w.apply = func(kube.ResourceList, kube.ResourceList, bool) (*kube.Result, error) {
		cancel()
		return &kube.Result{}, errors.New("partial apply failure")
	}
	recoveries := 0
	w.recoveryWait = func(kube.ResourceList, time.Duration, bool) error { recoveries++; return nil }
	if _, err := w.Update(nil, nil, false); err == nil {
		t.Fatal("partial apply unexpectedly succeeded")
	}
	if err := w.WaitWithJobs(nil, time.Second); err != nil {
		t.Fatal("cancelled caller prevented atomic recovery", err)
	}
	if recoveries != 1 {
		t.Fatalf("recovery calls=%d", recoveries)
	}
}

func TestHelmRecoveryIdentifiesPriorRevisionBeforeApply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &helmActionWaiter{ctx: ctx, deploymentID: "candidate", interval: time.Millisecond}
	recoveries := 0
	w.recoveryWait = func(kube.ResourceList, time.Duration, bool) error { recoveries++; return nil }
	previous := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"dispatch.app/deployment-id": "previous"}}}
	if err := w.Wait(kube.ResourceList{&resource.Info{Object: previous}}, time.Second); err != nil {
		t.Fatal("prior revision did not receive bounded recovery", err)
	}
	if recoveries != 1 {
		t.Fatalf("recovery calls=%d", recoveries)
	}
}

func TestHelmUnlabelledCandidateWaitStillHonorsCancellation(t *testing.T) {
	for _, labels := range [][]string{{""}, {"", "candidate"}, {"previous", "candidate"}} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		w := &helmActionWaiter{ctx: ctx, deploymentID: "candidate", interval: time.Millisecond}
		w.recoveryWait = func(kube.ResourceList, time.Duration, bool) error {
			t.Error("candidate was misidentified as atomic recovery")
			return nil
		}
		var resources kube.ResourceList
		for _, id := range labels {
			object := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"dispatch.app/deployment-id": id}}}
			resources = append(resources, &resource.Info{Object: object})
		}
		if err := w.WaitWithJobs(resources, time.Second); !errors.Is(err, context.Canceled) {
			t.Fatalf("labels=%v cancellation result: %v", labels, err)
		}
	}
}

func TestHelmHookDeletionWaitFailureAllowsSameDeploymentRecovery(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "hook-still-present", true: "hook-deleted"}[deleted], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var requests atomic.Int32
			client := &restfake.RESTClient{
				NegotiatedSerializer: resource.UnstructuredPlusDefaultContentConfig().NegotiatedSerializer,
				GroupVersion:         schema.GroupVersion{Version: "v1"}, VersionedAPIPath: "/api/v1",
				Client: restfake.CreateHTTPClient(func(r *http.Request) (*http.Response, error) {
					requests.Add(1)
					if r.Method != "GET" || r.URL.Path != "/api/v1/namespaces/default/pods/hook" {
						t.Errorf("unexpected hook inspection: %s %s", r.Method, r.URL.Path)
					}
					code, body := http.StatusOK, `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"hook","namespace":"default"}}`
					if deleted {
						code, body = http.StatusNotFound, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"NotFound","code":404}`
					}
					return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				}),
			}
			resources := kube.ResourceList{&resource.Info{Name: "hook", Namespace: "default", Client: client, Mapping: &meta.RESTMapping{Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Scope: meta.RESTScopeNamespace}}}
			w := &helmActionWaiter{Client: &kube.Client{Log: func(string, ...interface{}) {}}, ctx: ctx, deploymentID: "same-deployment"}
			w.apply = func(kube.ResourceList, kube.ResourceList, bool) (*kube.Result, error) { return &kube.Result{}, nil }
			waitErr := w.WaitForDelete(resources, 25*time.Millisecond)
			if deleted && waitErr != nil || !deleted && !errors.Is(waitErr, context.DeadlineExceeded) || requests.Load() == 0 {
				t.Fatalf("deleted=%v requests=%d error=%v", deleted, requests.Load(), waitErr)
			}
			recoveries := 0
			w.recoveryWait = func(kube.ResourceList, time.Duration, bool) error { recoveries++; return nil }
			previous := kube.ResourceList{&resource.Info{Object: &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"dispatch.app/deployment-id": "same-deployment"}}}}}
			// A failed pre-upgrade hook reaches rollback before the first Update.
			if _, err := w.Update(nil, previous, false); err != nil {
				t.Fatal(err)
			}
			err := w.WaitWithJobs(previous, time.Second)
			if deleted {
				if !errors.Is(err, context.Canceled) || recoveries != 0 {
					t.Fatal("successful hook cleanup bypassed candidate cancellation", err, recoveries)
				}
			} else if err != nil || recoveries != 1 {
				t.Fatal("hook deletion failure did not allow bounded rollback", err, recoveries)
			}
		})
	}
}

func TestHelmSecondUpdateAllowsSameDeploymentRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &helmActionWaiter{ctx: ctx, deploymentID: "same-deployment"}
	w.apply = func(kube.ResourceList, kube.ResourceList, bool) (*kube.Result, error) { return &kube.Result{}, nil }
	resources := kube.ResourceList{&resource.Info{Object: &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"dispatch.app/deployment-id": "same-deployment"}}}}}
	for range 2 {
		if _, err := w.Update(nil, resources, false); err != nil {
			t.Fatal(err)
		}
	}
	recoveries := 0
	w.recoveryWait = func(kube.ResourceList, time.Duration, bool) error { recoveries++; return nil }
	if err := w.WaitWithJobs(resources, time.Second); err != nil || recoveries != 1 {
		t.Fatal("same-deployment rollback did not use its independent wait", err, recoveries)
	}
}
