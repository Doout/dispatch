package githubapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRepositoryIdentityRecoveryNeverSwitchesNames(t *testing.T) {
	ctx := context.Background()
	name, id, archived, disabled, missing, deny := "owner/config", int64(42), false, false, false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/installations":
			if deny {
				w.WriteHeader(403)
				return
			}
			json.NewEncoder(w).Encode([]any{map[string]any{"id": 73, "account": map[string]string{"login": "owner"}}})
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			json.NewEncoder(w).Encode(map[string]any{"token": "private-fixture-token", "expires_at": time.Now().Add(time.Hour)})
		case r.URL.Path == "/installation/repositories":
			items := []any{}
			if !missing {
				items = append(items, map[string]any{"id": id, "full_name": name, "archived": archived, "disabled": disabled})
			}
			json.NewEncoder(w).Encode(map[string]any{"repositories": items})
		case strings.HasSuffix(r.URL.Path, "/installation"):
			json.NewEncoder(w).Encode(map[string]any{"id": 73, "app_id": 42})
		case strings.HasSuffix(r.URL.Path, "/branches"):
			w.WriteHeader(403)
			w.Write([]byte(`{"message":"private-fixture-token"}`))
		default:
			t.Error("unexpected request", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m := installationTestManager(t, server.URL)
	check := func(expected string, repository string, wanted int64) {
		t.Helper()
		status, err := m.CheckRepository(ctx, "app", repository, wanted)
		if err != nil || status.State != expected {
			t.Fatal(status, err)
		}
		if expected != "accessible" && RequireAccessible(status) == nil {
			t.Fatal("unsafe source accepted")
		}
	}
	check("accessible", name, 0)
	archived = true
	check("archived", name, id)
	archived = false
	disabled = true
	check("disabled", name, id)
	disabled = false
	name = "owner/renamed"
	check("renamed", "owner/config", 42)
	check("accessible", name, 42)
	id = 99
	name = "owner/config"
	check("identity_changed", name, 42)
	check("accessible", name, 99)
	missing = true
	check("inaccessible", name, 42)
	missing = false
	deny = true
	check("inaccessible", name, 42)
	deny = false
	if _, err := m.RepositoryBranches(ctx, "app", name, 99); err == nil || strings.Contains(err.Error(), "private-fixture-token") {
		t.Fatal("branch failure did not protect credentials", err)
	}
}
