package uipreview

import (
	"net/http"
	"net/http/httputil"
	"strings"
)

func (s *Server) workload(w http.ResponseWriter, r *http.Request) {
	proxy := &httputil.ReverseProxy{
		Transport:     s.transport,
		FlushInterval: -1,
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(s.development)
			p.Out.Host = r.Host
			p.Out.Header.Del("Forwarded")
			p.Out.Header.Del("X-Forwarded-Host")
			p.Out.Header.Del("X-Forwarded-For")
			p.Out.Header.Del("X-Forwarded-Proto")
			p.Out.Header.Del("Cookie")
			for _, cookie := range r.Cookies() {
				if cookie.Name == sourceCookie || cookie.Name == accessCookie || strings.HasPrefix(cookie.Name, "__Host-dispatch-") {
					continue
				}
				p.Out.AddCookie(cookie)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			problem(w, 502, "Development workload unavailable.")
		},
	}
	w.Header().Del("Content-Security-Policy")
	proxy.ServeHTTP(w, r)
}
