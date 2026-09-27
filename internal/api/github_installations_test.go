package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/githubapp"
)

func TestAppWebhookAcceptsMatchingInstallationsAcrossOrganizations(t *testing.T) {
	a := serviceTestAPI(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/alpha/service/installation":
			fmt.Fprint(w, `{"id":73,"app_id":42}`)
		case "/repos/beta/ui/installation":
			fmt.Fprint(w, `{"id":74,"app_id":42}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	keyPath := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("k", 31)+"!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	manager := githubapp.New(a.store, vault)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	secret := "0123456789abcdef"
	encrypted, webhook, err := manager.EncryptCredentials("app", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), secret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	err = a.store.CreateGitHubApp(context.Background(), core.GitHubAppConnection{ID: "app", Name: "App", AppID: 42, InstallationID: 73, APIURL: server.URL, WebURL: server.URL, EncryptedPrivateKey: encrypted, EncryptedWebhookSecret: webhook, State: "ready", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	a.eventConfig.GitHubApps = manager
	for _, test := range []struct {
		name, repository string
		installation     int
		validSignature   bool
		want             int
	}{
		{"first organization", "alpha/service", 73, true, 204},
		{"second organization", "beta/ui", 74, true, 204},
		{"another repository's installation", "beta/ui", 73, true, 403},
		{"missing installation", "alpha/service", 0, true, 403},
		{"invalid signature", "beta/ui", 74, false, 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"installation":{"id":%d},"repository":{"full_name":%q}}`, test.installation, test.repository)
			request := httptest.NewRequest("POST", "/", strings.NewReader(body))
			request.Header.Set("X-GitHub-Event", "ping")
			request.Header.Set("X-GitHub-Delivery", "test-delivery")
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(body))
			signature := hex.EncodeToString(mac.Sum(nil))
			if !test.validSignature {
				signature = strings.Repeat("0", 64)
			}
			request.Header.Set("X-Hub-Signature-256", "sha256="+signature)
			response := httptest.NewRecorder()
			a.processGitHubWebhook(response, request, secret, "app", 73, nil, nil)
			if response.Code != test.want {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
		})
	}
}
