package hosted

import (
	"net/http"
	"testing"
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
