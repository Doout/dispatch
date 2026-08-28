package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
		})
	}
}
