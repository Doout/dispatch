// Package uipreview serves development UI assets against a selected backend.
// It has no database connection and does not run controllers or workers.
package uipreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/ui"
)

const sourceCookie = "dispatch-ui-source"
const productionCookiePrefix = "__Host-dispatch-ui-production-"
const sourcePath = "/__dispatch_ui/data-source"

var previewWorkloadPrefix = regexp.MustCompile(`^p[0-9]+-`)

var labelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type Config struct {
	Origin      string
	Development string
	Production  string
	AccessKey   string
}

type Server struct {
	origin      *url.URL
	development *url.URL
	production  *url.URL
	assets      http.Handler
	transport   http.RoundTripper
	accessKey   [32]byte
	now         func() time.Time
}

func New(config Config) (*Server, error) {
	if len(config.AccessKey) < 32 || len(config.AccessKey) > 512 {
		return nil, errors.New("a preview access key of 32 to 512 bytes is required")
	}
	origin, err := parseOrigin(config.Origin, false)
	if err != nil {
		return nil, errors.New("preview origin must be an HTTPS origin")
	}
	development, err := parseOrigin(config.Development, true)
	if err != nil {
		return nil, errors.New("development backend must be an origin; HTTP is allowed only on loopback")
	}
	production, err := parseOrigin(config.Production, false)
	if err != nil || production.Hostname() == origin.Hostname() {
		return nil, errors.New("production backend must be a different HTTPS origin")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	server := &Server{origin: origin, development: development, production: production, transport: transport, accessKey: sha256.Sum256([]byte(config.AccessKey)), now: time.Now}
	server.assets = ui.HandlerWithHeadMarkup(server.headMarkup)
	return server, nil
}

func parseOrigin(raw string, loopback bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid origin")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if !loopback || u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return nil, errors.New("HTTPS required")
		}
	}
	u.Path = ""
	return u, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'; object-src 'none'")
	slug, valid := s.tenantSlug(r.Host)
	if !valid {
		http.NotFound(w, r)
		return
	}
	// Workload hosts belong to the dev platform, including their page and
	// assets. The production data selector applies only to the console UI.
	if previewWorkloadPrefix.MatchString(slug) || strings.HasPrefix(slug, "pr-") || strings.HasPrefix(slug, "app-") {
		s.workload(w, r)
		return
	}
	expectedOrigin := s.origin.Scheme + "://" + r.Host
	if origin := r.Header.Get("Origin"); origin != "" && origin != expectedOrigin {
		problem(w, http.StatusForbidden, "Origin not allowed.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Cookie") != "" && r.Header.Get("Origin") == "" {
		problem(w, http.StatusForbidden, "Origin required.")
		return
	}
	production := false
	if cookie, err := r.Cookie(sourceCookie); err == nil {
		production = cookie.Value == "production"
	}
	if r.URL.Path == sourcePath {
		s.switchSource(w, r)
		return
	}
	if r.URL.Path == accessPath {
		s.access(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/readyz" && r.URL.Path != "/healthz" {
		s.assets.ServeHTTP(w, r)
		return
	}
	if production {
		expires, allowed := s.productionAccess(r)
		if !allowed {
			problem(w, http.StatusForbidden, "Enter the preview access key to use production data.")
			return
		}
		ctx, cancel := context.WithDeadline(r.Context(), expires)
		defer cancel()
		r = r.Clone(ctx)
	}
	if production && !readAllowed(r, slug == "") {
		problem(w, http.StatusForbidden, "Production data is read only here. Switch to Development to make changes.")
		return
	}
	target := *s.development
	backendHost := r.Host
	backendOrigin := expectedOrigin
	if production {
		target = *s.production
		if slug != "" {
			target.Host = slug + "." + target.Host
		}
		backendHost = target.Host
		backendOrigin = target.Scheme + "://" + target.Host
	}
	proxy := &httputil.ReverseProxy{
		Transport:     s.transport,
		FlushInterval: -1,
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(&target)
			p.Out.Host = backendHost
			p.Out.Header.Del("Forwarded")
			p.Out.Header.Del("X-Forwarded-For")
			p.Out.Header.Del("X-Forwarded-Host")
			p.Out.Header.Del("X-Forwarded-Proto")
			p.Out.Header.Del("Referer")
			if r.Header.Get("Origin") != "" {
				p.Out.Header.Set("Origin", backendOrigin)
			}
			p.Out.Header.Del("Cookie")
			for _, cookie := range r.Cookies() {
				if production {
					if !strings.HasPrefix(cookie.Name, productionCookiePrefix) {
						continue
					}
					name, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cookie.Name, productionCookiePrefix))
					if err != nil {
						continue
					}
					cookie.Name = string(name)
				} else if cookie.Name == sourceCookie || cookie.Name == accessCookie || strings.HasPrefix(cookie.Name, productionCookiePrefix) {
					continue
				}
				p.Out.AddCookie(cookie)
			}
			if production && rewriteJSON(r.URL.Path) {
				p.Out.Header.Set("Accept-Encoding", "identity")
			}
		},
		ModifyResponse: func(response *http.Response) error {
			response.Header.Set("Cache-Control", "no-store")
			if !production {
				return nil
			}
			cookies := response.Cookies()
			response.Header.Del("Set-Cookie")
			for _, cookie := range cookies {
				cookie.Name = productionCookiePrefix + base64.RawURLEncoding.EncodeToString([]byte(cookie.Name))
				cookie.Domain = ""
				cookie.Path = "/"
				cookie.Secure = true
				cookie.HttpOnly = true
				response.Header.Add("Set-Cookie", cookie.String())
			}
			if location := response.Header.Get("Location"); location != "" {
				response.Header.Set("Location", s.localURL(location))
			}
			if response.StatusCode >= 200 && response.StatusCode < 300 && rewriteJSON(r.URL.Path) {
				return s.rewriteResponse(response, r.URL.Path)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			problem(w, http.StatusBadGateway, "The selected backend is unavailable.")
		},
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) tenantSlug(host string) (string, bool) {
	if host == s.origin.Host {
		return "", true
	}
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		name = host
		port = ""
	}
	if port != s.origin.Port() {
		return "", false
	}
	slug, found := strings.CutSuffix(name, "."+s.origin.Hostname())
	return slug, found && labelPattern.MatchString(slug)
}

func readAllowed(r *http.Request, platform bool) bool {
	if r.Header.Get("Upgrade") != "" {
		return false
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		// These GET routes change credentials or lease work. They are not reads.
		cleaned := path.Clean(r.URL.Path)
		switch cleaned {
		case "/api/v1/auth/callback", "/api/v1/auth/providers/manifest/callback", "/api/v1/github-apps/manifest/callback", "/api/v1/laneway-applications/setup", "/api/v1/laneway-networks/callback":
			return false
		}
		if strings.HasPrefix(cleaned, "/api/v1/edge/nodes/") && strings.HasSuffix(cleaned, "/jobs/next") {
			return false
		}
		return true
	}
	if r.Method != http.MethodPost || r.URL.RawPath != "" {
		return false
	}
	if platform {
		if r.URL.Path == "/api/v1/account/workos/authenticate" {
			// Signing in is allowed; linking accounts and accepting invitations
			// must happen on production itself, outside the read-only preview.
			if r.Body == nil || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				return false
			}
			raw, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
			r.Body = io.NopCloser(bytes.NewReader(raw))
			if err != nil || len(raw) > 64<<10 {
				return false
			}
			var in map[string]json.RawMessage
			if json.Unmarshal(raw, &in) != nil {
				return false
			}
			for key := range in {
				switch key {
				case "method", "email", "password", "code", "state", "organizationId", "tenantId", "codeChallenge", "resume":
				default:
					return false
				}
			}
			return true
		}
		return r.URL.Path == "/api/v1/account/login" || r.URL.Path == "/api/v1/account/logout" || r.URL.Path == "/api/v1/account/handoffs"
	}
	return r.URL.Path == "/api/v1/hosted/auth/exchange" || r.URL.Path == "/api/v1/hosted/auth/logout"
}

func (s *Server) switchSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		problem(w, http.StatusMethodNotAllowed, "Use POST to select a data source.")
		return
	}
	if r.Header.Get("Origin") != s.origin.Scheme+"://"+r.Host {
		problem(w, http.StatusForbidden, "Origin required.")
		return
	}
	var input struct {
		Source string `json:"source"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || (input.Source != "development" && input.Source != "production") || decoder.Decode(new(any)) != io.EOF {
		problem(w, http.StatusBadRequest, "Choose Development or Production.")
		return
	}
	if _, allowed := s.productionAccess(r); input.Source == "production" && !allowed {
		problem(w, http.StatusForbidden, "Enter the preview access key to use production data.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sourceCookie, Value: input.Source, Domain: s.origin.Hostname(), Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 86400 * 30})
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func rewriteJSON(path string) bool {
	return path == "/api/v1/hosted" || path == "/api/v1/account/tenants" || path == "/api/v1/account/handoffs"
}

func (s *Server) localURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Scheme != s.production.Scheme {
		return raw
	}
	if u.Host == s.production.Host {
		u.Host = s.origin.Host
	} else {
		slug, found := strings.CutSuffix(u.Hostname(), "."+s.production.Hostname())
		if !found || !labelPattern.MatchString(slug) || u.Port() != s.production.Port() {
			return raw
		}
		u.Host = slug + "." + s.origin.Host
	}
	u.Scheme = s.origin.Scheme
	return u.String()
}

func (s *Server) rewriteResponse(response *http.Response, path string) error {
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(contents) > 1<<20 {
		return errors.New("preview response exceeds limit")
	}
	var data map[string]any
	if err = json.Unmarshal(contents, &data); err != nil {
		return err
	}
	if path == "/api/v1/hosted" {
		if origin, ok := data["origin"].(string); ok {
			data["dataOrigin"] = origin
			data["origin"] = s.localURL(origin)
		}
		if origin, ok := data["loginUrl"].(string); ok {
			data["loginUrl"] = s.localURL(origin)
		}
	} else if path == "/api/v1/account/handoffs" {
		if target, ok := data["url"].(string); ok {
			data["url"] = s.localURL(target)
		}
	} else if memberships, ok := data["memberships"].([]any); ok {
		for _, entry := range memberships {
			if item, ok := entry.(map[string]any); ok {
				if target, ok := item["url"].(string); ok {
					item["url"] = s.localURL(target)
				}
			}
		}
	}
	contents, err = json.Marshal(data)
	if err != nil {
		return err
	}
	response.Body = io.NopCloser(bytes.NewReader(contents))
	response.ContentLength = int64(len(contents))
	response.Header.Del("Content-Length")
	response.Header.Del("ETag")
	response.Header.Set("Cache-Control", "no-store")
	return nil
}

func (s *Server) headMarkup(r *http.Request) []byte {
	source := "development"
	upstream := s.origin.String()
	if cookie, err := r.Cookie(sourceCookie); err == nil && cookie.Value == "production" {
		source = "production"
		upstream = s.production.String()
	}
	_, allowed := s.productionAccess(r)
	metadata, _ := json.Marshal(map[string]any{"source": source, "upstream": upstream, "readOnly": source == "production", "productionAccess": allowed})
	script := append([]byte(`<script id="dispatch-ui-preview" type="application/json">`), metadata...)
	return append(script, []byte("</script>")...)
}

func problem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "detail": detail})
}
