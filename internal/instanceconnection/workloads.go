package instanceconnection

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/store"
)

func (m *Manager) RoutingScope() *core.HostedRoutingScope {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.current.Configured || m.current.InstanceSlug == "" || m.current.RootDomain == "" {
		return nil
	}
	scope := &core.HostedRoutingScope{TenantID: m.current.InstanceID, TenantSlug: m.current.InstanceSlug, RootDomain: m.current.RootDomain}
	if _, err := routing.HostedHostname(*scope, core.App{ID: "validation", ProjectID: "validation"}); err != nil {
		return nil
	}
	return scope
}

func (m *Manager) PublicOrigin() string {
	if scope := m.RoutingScope(); scope != nil {
		return "https://" + scope.TenantSlug + "." + scope.RootDomain
	}
	return ""
}
func (m *Manager) ownedRoute(ctx context.Context, route core.ApplicationRoute, scope core.HostedRoutingScope) (core.App, error) {
	if m.config.LookupApp == nil {
		return core.App{}, errors.New("application lookup is unavailable")
	}
	app, err := m.config.LookupApp(ctx, route.AppID)
	if err != nil || app.State == "closed" || app.ProjectID != route.ProjectID || app.ServerID != route.ServerID {
		return core.App{}, errors.New("application route ownership is invalid")
	}
	hostname, err := routing.HostedHostname(scope, app)
	if err != nil || hostname != route.Hostname {
		return core.App{}, errors.New("application hostname does not belong to this instance")
	}
	return app, nil
}
func (m *Manager) RegisterRoute(ctx context.Context, route core.ApplicationRoute) error {
	scope := m.RoutingScope()
	if scope == nil {
		return errors.New("instance is not connected")
	}
	app, err := m.ownedRoute(ctx, route, *scope)
	if err != nil {
		return err
	}
	m.mu.RLock()
	s := m.current
	m.mu.RUnlock()
	previewNumber := 0
	if app.Generated && len(app.HelmProvenance.PullRequests) > 0 {
		previewNumber = app.HelmProvenance.PullRequests[0].Number
	}
	var result struct {
		Hostname string `json:"hostname"`
	}
	if err = m.call(ctx, http.MethodPost, s.ControlPlaneURL+"/api/v1/instance-connections/routes", s.Credential, map[string]any{"appID": app.ID, "projectID": app.ProjectID, "serverID": app.ServerID, "previewNumber": previewNumber}, &result); err != nil {
		return err
	}
	if result.Hostname != route.Hostname {
		return errors.New("control plane returned a different application hostname")
	}
	return nil
}
func (m *Manager) reconcileRoutes(ctx context.Context, s saved) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	type published struct {
		AppID    string `json:"appID"`
		ServerID string `json:"serverID"`
		Hostname string `json:"hostname"`
	}
	scope := core.HostedRoutingScope{TenantID: s.InstanceID, TenantSlug: s.InstanceSlug, RootDomain: s.RootDomain}
	for ctx.Err() == nil {
		routes, err := m.config.ListRoutes(ctx)
		if err == nil {
			current := map[string]string{}
			validRoutes := []core.ApplicationRoute{}
			for _, route := range routes {
				if route.RequestedDeploymentID == "" && route.DeploymentID == "" {
					continue
				}
				if _, err = m.ownedRoute(ctx, route, scope); err != nil {
					continue
				}
				current[route.AppID+":"+route.ServerID] = route.Hostname
				validRoutes = append(validRoutes, route)
			}
			var remote []published
			if err = m.call(ctx, http.MethodGet, s.ControlPlaneURL+"/api/v1/instance-connections/routes", s.Credential, nil, &remote); err == nil {
				known := map[string]string{}
				for _, old := range remote {
					known[old.AppID+":"+old.ServerID] = old.Hostname
					if current[old.AppID+":"+old.ServerID] == old.Hostname {
						continue
					}
					// A deployment can reserve a route after the list snapshot. Check
					// current local ownership before deleting its new reservation.
					if m.config.LookupRoute != nil {
						latest, lookupErr := m.config.LookupRoute(ctx, old.AppID)
						if lookupErr != nil && !errors.Is(lookupErr, store.ErrNotFound) {
							continue
						}
						if lookupErr == nil && latest.ServerID == old.ServerID && latest.Hostname == old.Hostname {
							if _, ownershipErr := m.ownedRoute(ctx, latest, scope); ownershipErr == nil {
								continue
							}
						}
					}
					if err := m.call(ctx, http.MethodDelete, s.ControlPlaneURL+"/api/v1/instance-connections/routes", s.Credential, map[string]any{"appID": old.AppID, "serverID": old.ServerID}, nil); err == nil {
						delete(known, old.AppID+":"+old.ServerID)
					}
				}
				for _, route := range validRoutes {
					if known[route.AppID+":"+route.ServerID] != route.Hostname {
						_ = m.RegisterRoute(ctx, route)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (m *Manager) serveWorkload(w http.ResponseWriter, r *http.Request) {
	if m.config.ListRoutes == nil && m.config.LookupRouteByHostname == nil {
		http.Error(w, "Application routing is unavailable.", 503)
		return
	}
	scope := m.RoutingScope()
	if scope == nil {
		http.Error(w, "Instance is disconnected.", 503)
		return
	}
	hostname, err := routing.NormalizeHostname(r.Host)
	if err != nil {
		http.Error(w, "Application host is invalid.", 400)
		return
	}
	var routes []core.ApplicationRoute
	if m.config.LookupRouteByHostname != nil {
		route, lookupErr := m.config.LookupRouteByHostname(r.Context(), hostname)
		if errors.Is(lookupErr, store.ErrNotFound) {
			http.Error(w, "Application route is unavailable.", 404)
			return
		}
		err = lookupErr
		routes = []core.ApplicationRoute{route}
	} else {
		routes, err = m.config.ListRoutes(r.Context())
	}
	if err != nil {
		http.Error(w, "Application routing is unavailable.", 503)
		return
	}
	var selected *core.ApplicationRoute
	for _, route := range routes {
		if route.Hostname != hostname || route.DeploymentID == "" || route.Destination == "" {
			continue
		}
		if _, err = m.ownedRoute(r.Context(), route, *scope); err != nil {
			continue
		}
		copy := route
		selected = &copy
		break
	}
	if selected == nil {
		http.Error(w, "Application route is unavailable.", 404)
		return
	}
	destination, err := url.Parse(selected.Destination)
	if err != nil || destination.Host == "" || destination.User != nil || destination.RawQuery != "" || destination.Fragment != "" || destination.Scheme != "http" && destination.Scheme != "https" {
		http.Error(w, "Application destination is invalid.", 503)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(destination)
	original := proxy.Director
	proxy.Director = func(out *http.Request) {
		original(out)
		out.Host = hostname
		out.Header.Del("X-Dispatch-Workload-Host")
		out.Header.Del("Forwarded")
		for name := range out.Header {
			if strings.HasPrefix(strings.ToLower(name), "x-forwarded-") {
				out.Header.Del(name)
			}
		}
		out.Header.Set("X-Forwarded-Host", hostname)
		out.Header.Set("X-Forwarded-Proto", "https")
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "Application is unavailable.", 502)
	}
	proxy.FlushInterval = -1
	proxy.ServeHTTP(w, r)
}
