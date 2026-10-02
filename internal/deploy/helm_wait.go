package deploy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"helm.sh/helm/v3/pkg/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
func (c *sdkHelmClient) actionContext(ctx context.Context) (context.Context, func(), error) {
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
	wrapped := &helmActionWaiter{Client: client, ctx: ctx, interval: 2 * time.Second}
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
}

func (w *helmActionWaiter) Wait(resources kube.ResourceList, timeout time.Duration) error {
	return w.wait(resources, timeout, false)
}
func (w *helmActionWaiter) WaitWithJobs(resources kube.ResourceList, timeout time.Duration) error {
	return w.wait(resources, timeout, true)
}
func (w *helmActionWaiter) wait(resources kube.ResourceList, timeout time.Duration, jobs bool) error {
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
