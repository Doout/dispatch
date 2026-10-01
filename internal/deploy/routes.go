package deploy

import (
	"context"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/store"
)

type routeReporterKey struct{}
type RouteReporter func(context.Context, core.ApplicationRoute) error

func WithRouteReporter(ctx context.Context, report RouteReporter) context.Context {
	return context.WithValue(ctx, routeReporterKey{}, report)
}

// ReportRoute is also used by the enrolled runtime adapter after validating the
// typed result against its accepted request. No route filesystem path crosses it.
func ReportRoute(ctx context.Context, route core.ApplicationRoute) error {
	if report, ok := ctx.Value(routeReporterKey{}).(RouteReporter); ok {
		return report(ctx, route)
	}
	return nil
}

func (s *Service) reserveRoute(ctx context.Context, d core.Deployment, app core.App, server core.Server) error {
	if server.Routing == nil || app.BuildType == core.BuildTypeHelm {
		return nil
	}
	plan, err := routing.Plan(d, app, server)
	if err != nil {
		return err
	}
	if plan == nil {
		return nil
	}
	data, ok := s.store.(store.ApplicationRouteStore)
	if !ok {
		return errors.New("application route storage is unavailable")
	}
	_, err = data.ReserveApplicationRoute(ctx, *plan)
	return err
}
func (s *Service) routeContext(ctx context.Context) context.Context {
	data, ok := s.store.(store.ApplicationRouteStore)
	if !ok {
		return ctx
	}
	return WithRouteReporter(ctx, data.SaveApplicationRoute)
}
func (s *Service) routeCompletion(ctx context.Context, app core.App, d core.Deployment) string {
	data, ok := s.store.(store.ApplicationRouteStore)
	if !ok {
		return "Deployment is live"
	}
	route, err := data.GetApplicationRoute(ctx, app.ID)
	if errors.Is(err, store.ErrNotFound) {
		return "Workload is ready"
	}
	if err != nil {
		return "Workload is ready; route evidence is unavailable"
	}
	if route.DeploymentID != d.ID {
		return "Workload is ready; managed route was not promoted"
	}
	route = routing.Probe{}.Check(ctx, route)
	if err = data.SaveApplicationRoute(ctx, route); err != nil {
		return "Workload is ready; public route evidence could not be saved"
	}
	if route.State == "active" {
		return "Deployment is live at " + route.Hostname
	}
	return "Workload is ready; " + route.Message
}

func (s *Service) RunRouteReconciliation(ctx context.Context) {
	data, ok := s.store.(store.ApplicationRouteStore)
	if !ok {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		routes, err := data.ListApplicationRoutes(ctx)
		if err == nil {
			for _, route := range routes {
				if ctx.Err() != nil {
					return
				}
				if route.RequestedDeploymentID == "" {
					continue
				}
				checked := routing.Probe{}.Check(ctx, route)
				_ = data.SaveApplicationRoute(ctx, checked)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
