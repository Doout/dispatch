package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesRootWithoutRedirect(t *testing.T) {
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if location := response.Header().Get("Location"); location != "" {
		t.Fatalf("unexpected redirect to %q", location)
	}
	if !strings.Contains(response.Body.String(), `<div id="root"></div>`) {
		t.Fatal("embedded application shell was not served")
	}
}

func TestHandlerFallsBackToApplicationShell(t *testing.T) {
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/applications/example", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected SPA fallback, got %d", response.Code)
	}
}
