package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
)

func TestOverviewRequiresConfiguredToken(t *testing.T) {
	handler, cleanup := testHandler(t, "secret")
	defer cleanup()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	if !overview.Demo || len(overview.Apps) != 3 {
		t.Fatalf("unexpected overview: demo=%v apps=%d", overview.Demo, len(overview.Apps))
	}
}

func TestHealthDoesNotRequireToken(t *testing.T) {
	handler, cleanup := testHandler(t, "secret")
	defer cleanup()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

func testHandler(t *testing.T, token string) (http.Handler, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := data.Migrate(ctx); err != nil {
		cancel()
		_ = data.Close()
		t.Fatal(err)
	}
	if err := data.SeedDemo(ctx); err != nil {
		cancel()
		_ = data.Close()
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(data, deploy.NewService(data, deploy.SimulationExecutor{Delay: time.Millisecond}), true, token, logger)
	return handler, func() { cancel(); _ = data.Close() }
}
