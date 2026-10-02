package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"helm.sh/helm/v3/pkg/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/resource"
)

type helmReadinessChecker interface {
	IsReady(context.Context, *resource.Info) (bool, error)
}

// Helm's context listener can return while its apply/readiness goroutine still
// runs. A later failure can then roll back a newer operation. Keep one failure
// path: the candidate wait observes cancellation synchronously, and Helm finishes
// its bounded atomic recovery before the action returns.
func (c *sdkHelmClient) actionContext(ctx context.Context, deploymentID string) (context.Context, func(), error) {
	client, ok := c.configuration.KubeClient.(*kube.Client)
	if !ok {
		// Injected clients supply their own action behavior in contract fixtures.
		return ctx, func() {}, nil
	}
	if client.Factory == nil {
		return nil, nil, errors.New("Kubernetes readiness client is unavailable")
	}
	clientset, err := client.Factory.KubernetesClientSet()
	if err != nil {
		return nil, nil, fmt.Errorf("initialize Kubernetes readiness client: %w", err)
	}
	wrapped := &helmActionWaiter{Client: client, ctx: ctx, deploymentID: deploymentID, interval: 2 * time.Second, apply: client.Update}
	wrapped.checker = func(jobs bool) helmReadinessChecker {
		checker := kube.NewReadyChecker(clientset, client.Log, kube.PausedAsReady(true), kube.CheckJobs(jobs))
		return &checker
	}
	wrapped.recoveryWait = func(resources kube.ResourceList, timeout time.Duration, jobs bool) error {
		if jobs {
			return client.WaitWithJobs(resources, timeout)
		}
		return client.Wait(resources, timeout)
	}
	c.configuration.KubeClient = wrapped
	return context.WithoutCancel(ctx), func() { c.configuration.KubeClient = client }, nil
}

// Embed the concrete client so Helm's optional deletion, merge, log and resource
// interfaces remain available during atomic uninstall and rollback.
type helmActionWaiter struct {
	*kube.Client
	ctx          context.Context
	interval     time.Duration
	waited       atomic.Bool
	checker      func(bool) helmReadinessChecker
	recoveryWait func(kube.ResourceList, time.Duration, bool) error
	deploymentID string
	applies      atomic.Int32
	apply        func(kube.ResourceList, kube.ResourceList, bool) (*kube.Result, error)
}

// A partial apply failure can enter atomic rollback before readiness starts.
func (w *helmActionWaiter) Update(original, target kube.ResourceList, force bool) (*kube.Result, error) {
	if w.applies.Add(1) > 1 || w.previousRevision(target) {
		w.waited.Store(true)
	}
	result, err := w.apply(original, target, force)
	if err != nil {
		w.waited.Store(true)
	}
	return result, err
}

// Legacy targets can run chart hooks before candidate apply. Hook failures also
// enter atomic recovery before the first readiness call, including retries of
// the same accepted deployment whose provenance ID has not changed.
func (w *helmActionWaiter) Create(resources kube.ResourceList) (*kube.Result, error) {
	result, err := w.Client.Create(resources)
	if err != nil {
		w.waited.Store(true)
	}
	return result, err
}
func (w *helmActionWaiter) Build(reader io.Reader, validate bool) (kube.ResourceList, error) {
	resources, err := w.Client.Build(reader, validate)
	if err != nil {
		w.waited.Store(true)
	}
	return resources, err
}
func (w *helmActionWaiter) WatchUntilReady(resources kube.ResourceList, timeout time.Duration) error {
	err := w.Client.WatchUntilReady(resources, timeout)
	if err != nil {
		w.waited.Store(true)
	}
	return err
}
func (w *helmActionWaiter) WaitForDelete(resources kube.ResourceList, timeout time.Duration) error {
	err := w.Client.WaitForDelete(resources, timeout)
	if err != nil {
		w.waited.Store(true)
	}
	return err
}
func (w *helmActionWaiter) Delete(resources kube.ResourceList) (*kube.Result, []error) {
	result, errs := w.Client.Delete(resources)
	if len(errs) > 0 {
		w.waited.Store(true)
	}
	return result, errs
}
func (w *helmActionWaiter) DeleteWithPropagationPolicy(resources kube.ResourceList, policy metav1.DeletionPropagation) (*kube.Result, []error) {
	result, errs := w.Client.DeleteWithPropagationPolicy(resources, policy)
	if len(errs) > 0 {
		w.waited.Store(true)
	}
	return result, errs
}

func (w *helmActionWaiter) previousRevision(resources kube.ResourceList) bool {
	if w.deploymentID == "" || len(resources) == 0 {
		return false
	}
	previous := false
	for _, info := range resources {
		object, err := meta.Accessor(info.Object)
		if err == nil {
			id := object.GetLabels()["dispatch.app/deployment-id"]
			if id == w.deploymentID {
				return false
			}
			previous = previous || id != ""
		}
	}
	// Flattened legacy List objects can have unlabelled children. Missing labels
	// do not establish that a wait belongs to an earlier revision.
	return previous
}

func (w *helmActionWaiter) Wait(resources kube.ResourceList, timeout time.Duration) error {
	return w.wait(resources, timeout, false)
}
func (w *helmActionWaiter) WaitWithJobs(resources kube.ResourceList, timeout time.Duration) error {
	return w.wait(resources, timeout, true)
}
func (w *helmActionWaiter) wait(resources kube.ResourceList, timeout time.Duration, jobs bool) error {
	if w.previousRevision(resources) {
		w.waited.Store(true)
	}
	if !w.waited.CompareAndSwap(false, true) {
		// Recovery must remain able to restore the prior healthy release after the
		// caller cancels. Helm bounds this wait with the accepted operation timeout.
		return w.recoveryWait(resources, timeout, jobs)
	}
	if err := w.ctx.Err(); err != nil {
		return err
	}
	checker := w.checker(jobs)
	failures := make([]int, len(resources))
	return wait.PollUntilContextTimeout(w.ctx, w.interval, timeout, true, func(ctx context.Context) (bool, error) {
		for index, item := range resources {
			ready, err := checker.IsReady(ctx, item)
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if err != nil {
				if helmReadinessRetryable(err) {
					failures[index]++
					if failures[index] <= 30 {
						return false, nil
					}
				}
				return false, err
			}
			failures[index] = 0
			if !ready {
				return false, nil
			}
		}
		return true, nil
	})
}
func helmReadinessRetryable(err error) bool {
	var status *apierrors.StatusError
	if !errors.As(err, &status) {
		return true
	}
	code := status.ErrStatus.Code
	return code == 0 || code == http.StatusTooManyRequests || code >= 500 && code != http.StatusNotImplemented
}

var _ kube.Interface = (*helmActionWaiter)(nil)
var _ kube.InterfaceExt = (*helmActionWaiter)(nil)
var _ kube.InterfaceThreeWayMerge = (*helmActionWaiter)(nil)
var _ kube.InterfaceLogs = (*helmActionWaiter)(nil)
var _ kube.InterfaceDeletionPropagation = (*helmActionWaiter)(nil)
var _ kube.InterfaceResources = (*helmActionWaiter)(nil)
