package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
)

func installationTestManager(t *testing.T, endpoint string) *Manager {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("k", 31)+"!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil, vault)
	encrypted, webhook, err := m.EncryptCredentials("app", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	m.Store = testStore{connection: core.GitHubAppConnection{ID: "app", AppID: 42, APIURL: endpoint, InstallationID: 73, EncryptedPrivateKey: encrypted, EncryptedWebhookSecret: webhook}}
	return m
}

func TestRepositoryCredentialsSelectAndCacheEachInstallation(t *testing.T) {
	lookups, exchanges := map[string]int{}, map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/installation"):
			lookups[p]++
			if !strings.Contains(r.Header.Get("Authorization"), ".") {
				t.Error("lookup must use App JWT")
			}
			if p == "/repos/alpha/missing/installation" {
				http.NotFound(w, r)
				return
			}
			id := 73
			if strings.Contains(p, "/beta/") {
				id = 74
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "app_id": 42})
		case strings.HasSuffix(p, "/access_tokens"):
			exchanges[p]++
			token := "alpha-token"
			if strings.Contains(p, "/74/") {
				token = "beta-token"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": token, "expires_at": time.Now().Add(time.Hour)})
		case strings.Contains(p, "/commits/"):
			token := "alpha-token"
			if strings.Contains(p, "/beta/") {
				token = "beta-token"
			}
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Error("repository received another installation's token")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "commit"})
		default:
			t.Errorf("unexpected request %s", p)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m := installationTestManager(t, server.URL)
	for range 2 {
		for _, repo := range []string{"alpha/config", "beta/service"} {
			if _, err := m.RepositoryHead(context.Background(), "app", repo, "main"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(exchanges) != 2 || exchanges["/app/installations/73/access_tokens"] != 1 || exchanges["/app/installations/74/access_tokens"] != 1 {
		t.Fatal("tokens not cached per installation", exchanges)
	}
	if lookups["/repos/alpha/config/installation"] != 1 || lookups["/repos/beta/service/installation"] != 1 {
		t.Fatal("repository discovery was not cached", lookups)
	}
	if _, err := m.RepositoryToken(context.Background(), "app", "alpha/missing"); err == nil {
		t.Fatal("missing repository fell back to setup installation")
	}
	m.Invalidate("app")
	if _, err := m.RepositoryToken(context.Background(), "app", "beta/service"); err != nil {
		t.Fatal(err)
	}
	if exchanges["/app/installations/74/access_tokens"] != 2 || lookups["/repos/beta/service/installation"] != 2 {
		t.Fatal("invalidation retained credentials")
	}
}

func TestListRepositoriesAcrossAllInstallationPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations":
			items := []map[string]any{}
			if r.URL.Query().Get("page") == "1" {
				for id := 1; id <= 100; id++ {
					items = append(items, map[string]any{"id": id, "account": map[string]string{"login": fmt.Sprint(id)}})
				}
			} else if r.URL.Query().Get("page") == "2" {
				items = append(items, map[string]any{"id": 101, "account": map[string]string{"login": "last"}})
			} else {
				t.Fatal("unexpected installation page")
			}
			_ = json.NewEncoder(w).Encode(items)
		case "/installation/repositories":
			id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer token-")
			numeric, _ := strconv.Atoi(id)
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": []map[string]any{{"id": numeric, "full_name": "org" + id + "/repo"}}})
		default:
			if strings.HasSuffix(r.URL.Path, "/access_tokens") {
				id := strings.Split(r.URL.Path, "/")[3]
				_ = json.NewEncoder(w).Encode(map[string]any{"token": "token-" + id, "expires_at": time.Now().Add(time.Hour)})
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m := installationTestManager(t, server.URL)
	items, err := m.ListInstallations(context.Background(), "app")
	if err != nil || len(items) != 101 {
		t.Fatalf("pagination: %d %v", len(items), err)
	}
	repos, err := m.ListRepositories(context.Background(), "app")
	if err != nil || len(repos) != 101 || repos[100].FullName != "org101/repo" {
		t.Fatalf("repositories: %d %v", len(repos), err)
	}
}

func TestRepositoryInstallationRejectsSuspendedAndWrongApp(t *testing.T) {
	for _, payload := range []string{`{"id":73,"app_id":99}`, `{"id":73,"app_id":42,"suspended_at":"2026-01-01T00:00:00Z"}`} {
		t.Run(payload, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, payload) }))
			defer server.Close()
			m := installationTestManager(t, server.URL)
			if _, err := m.RepositoryToken(context.Background(), "app", "alpha/repo"); err == nil {
				t.Fatal("invalid installation accepted")
			}
		})
	}
}
