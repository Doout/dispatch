package hosted

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/doout/dispatch/internal/instancebridge"
	"github.com/doout/dispatch/internal/tenancy"
)

// The gateway is an operator-controlled ingress, not a tenant-supplied URL or
// the worker's private loopback destination. It selects a registered host using
// its own deployment routes. An outbound workload tunnel is a separate service.
func (s *Server) workload(w http.ResponseWriter, r *http.Request, host string) {
	if r.Header.Get("X-Dispatch-Workload-Hop") != "" {
		problem(w, 502, "Workload ingress routes back to the control plane.")
		return
	}
	owner, err := s.Catalog.WorkloadHost(r.Context(), host)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	tenant, err := s.Catalog.Tenant(r.Context(), owner.TenantID)
	if err != nil || tenant.State != tenancy.StateActive {
		http.NotFound(w, r)
		return
	}
	connection, err := s.Catalog.InstanceConnection(r.Context(), tenant.ID)
	if err != nil {
		problem(w, 503, "Workload unavailable.")
		return
	}
	if connection.BackendMode == tenancy.BackendConnected {
		copy := r.Clone(r.Context())
		copy.Header = r.Header.Clone()
		copy.Host = host
		w.Header().Del("Content-Security-Policy")
		w.Header().Del("X-Frame-Options")
		s.Bridge.ProxyOperation(w, copy, connectionKey(connection), instancebridge.OperationWorkload, instancebridge.Identity{})
		return
	}
	runtime, err := s.runtime(tenant)
	if err != nil {
		problem(w, 503, "Workload unavailable.")
		return
	}
	route, err := runtime.Store.GetApplicationRoute(r.Context(), owner.AppID)
	if err != nil || route.Hostname != host || route.ProjectID != owner.ProjectID || route.ServerID != owner.ServerID || route.DeploymentID == "" || route.Destination == "" {
		http.NotFound(w, r)
		return
	}
	app, err := runtime.Store.GetApp(r.Context(), owner.AppID)
	if err != nil || app.ServerID != owner.ServerID || app.ProjectID != owner.ProjectID {
		http.NotFound(w, r)
		return
	}
	gateway := s.Config.WorkloadGateway
	if gateway == "" || gateway == s.Config.RootDomain || gateway == host {
		problem(w, 503, "Workload ingress is not configured.")
		return
	}
	var target *url.URL
	if strings.Contains(gateway, "://") {
		target, err = url.Parse(gateway)
		if err != nil || target.Hostname() == host || target.Hostname() == s.Config.RootDomain {
			problem(w, 503, "Workload ingress is not configured.")
			return
		}
	} else {
		if net.ParseIP(gateway) != nil {
			gateway = net.JoinHostPort(gateway, "443")
		}
		target = &url.URL{Scheme: "https", Host: gateway}
	}
	proxy := &httputil.ReverseProxy{
		Transport: s.workloadTransport,
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			p.Out.Host = host
			p.Out.Header.Set("X-Dispatch-Workload-Hop", s.Config.RootDomain)
			p.SetXForwarded()
			p.Out.Header.Set("X-Forwarded-Proto", "https")
			p.Out.Header.Set("X-Forwarded-Host", host)
			// Console sessions are never credentials for a deployed application.
			p.Out.Header.Del("Cookie")
			for _, cookie := range p.In.Cookies() {
				if cookie.Name != tenantCookie && cookie.Name != platformCookie {
					p.Out.AddCookie(cookie)
				}
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			problem(w, 502, "Workload ingress unavailable.")
		},
	}
	// Application responses own their browser policy. Console-only headers
	// must not prevent a deployed application from embedding or using a base URL.
	w.Header().Del("Content-Security-Policy")
	w.Header().Del("X-Frame-Options")
	// No tenant identifier is accepted from forwarded headers. Host ownership is
	// checked above against the catalog and the tenant's current published route.
	proxy.ServeHTTP(w, r)
}
