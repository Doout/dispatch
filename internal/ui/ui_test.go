package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesAppManifest(t *testing.T) {
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/site.webmanifest", nil))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("manifest response = %d, %q", response.Code, response.Header().Get("Content-Type"))
	}
	if !json.Valid(response.Body.Bytes()) {
		t.Fatal("manifest must contain JSON, not the application entrypoint")
	}
}

func TestHandlerServesApplicationRoutes(t *testing.T) {
	for _, target := range []string{
		"/deployments/deployment-123",
		"/applications/templates",
		"/applications/helm-sources",
		"/applications/preview-groups",
		"/events/activity",
		"/projects",
		"/servers",
		"/secrets",
		"/connections",
	} {
		t.Run(target, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, target, nil)
			response := httptest.NewRecorder()
			Handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if !strings.Contains(response.Body.String(), `id="root"`) {
				t.Fatal("application route did not return the web entrypoint")
			}
			if strings.Contains(response.Body.String(), `id="dispatch-ui-preview"`) || response.Header().Get("Cache-Control") != "no-cache" {
				t.Fatal("regular installations must not receive preview metadata or cache policy")
			}
		})
	}
}
