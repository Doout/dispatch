package api

import (
	"bytes"
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

func TestApplicationRequiresReadyServer(t *testing.T) {
	handler, cleanup := testHandler(t, "")
	defer cleanup()

	serverBody := bytes.NewBufferString(`{"name":"remote-01","address":"10.0.0.8","runtime":"docker"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/servers", serverBody))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected server creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	appBody, err := json.Marshal(map[string]any{
		"projectId":  overview.Projects[0].ID,
		"serverId":   server.ID,
		"name":       "pending-app",
		"sourceRepo": "https://example.test/pending.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(appBody)))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected pending target to return 409, got %d: %s", response.Code, response.Body.String())
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
