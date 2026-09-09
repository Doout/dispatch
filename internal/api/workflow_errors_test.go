package api

import (
	"context"
	"encoding/json"
	"github.com/doout/dispatch/internal/core"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestConfigSyncReturnsProblemDetail(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	a := handler.(*API)
	projects, err := a.store.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.CreateSecret(context.Background(), core.Secret{ID: "repo-key", Name: "Repository key", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	source := core.ConfigSource{CredentialSecretID: "repo-key", ID: "broken-config", ProjectID: projects[0].ID, Name: "Broken config", Repository: "https://github.com/org/config.git", Branch: "main", Path: "deployment", SyncMode: core.ConfigSyncPoll, Active: true, State: "ready", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = a.store.CreateConfigSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/v1/config-sources/broken-config/sync", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("wrong error response: %d %s", response.Code, response.Body.String())
	}
	var body struct{ Title, Detail string }
	if err = json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Detail != "repository credential resolution is not configured" || body.Title != "Configuration sync blocked" {
		t.Fatalf("lost sync cause: %#v", body)
	}
}
