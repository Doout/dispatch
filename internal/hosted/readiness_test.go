package hosted

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/doout/dispatch/internal/tenancy"
)

func TestHostedReadinessTracksCatalogAndShutdown(t *testing.T) {
	for _, failure := range []string{"catalog", "shutdown"} {
		t.Run(failure, func(t *testing.T) {
			f := newHostedFixture(t)
			check := func(path string, status int) {
				t.Helper()
				w := f.request(t, f.server.Config.RootDomain, http.MethodGet, path, "", nil)
				if w.Code != status {
					t.Fatalf("%s status = %d, want %d", path, w.Code, status)
				}
				if path == "/readyz" && w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("readiness may be cached")
				}
			}
			check("/readyz", http.StatusOK)
			if failure == "catalog" {
				if err := f.catalog.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				f.server.Close()
			}
			check("/readyz", http.StatusServiceUnavailable)
			check("/healthz", http.StatusOK)
			if f.opens.Load() != 0 {
				t.Fatal("readiness opened a tenant store")
			}
		})
	}
}

func TestHostedReadinessRequiresInitializedCatalog(t *testing.T) {
	f := newHostedFixture(t)
	catalog, err := tenancy.Open(context.Background(), filepath.Join(t.TempDir(), "uninitialized.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	f.server.Catalog = catalog
	w := f.request(t, f.server.Config.RootDomain, http.MethodGet, "/readyz", "", nil)
	if w.Code != http.StatusServiceUnavailable || w.Body.String() != "{\"status\":\"unavailable\"}\n" {
		t.Fatalf("uninitialized catalog readiness = %d %q", w.Code, w.Body.String())
	}
	if f.opens.Load() != 0 {
		t.Fatal("readiness opened a tenant store")
	}
}
