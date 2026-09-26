package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/store"
)

func TestImportWorkflowPreviewDocumentFromPRHead(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "preview-import.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	masterKey := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(masterKey, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	document := "apiVersion: dispatch/v1alpha1\nkind: Application\nmetadata:\n  name: preview-__PREVIEW_ID__\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/installations/73/access_tokens":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "installation-token", "expires_at": time.Now().Add(time.Hour).UTC()})
		case "/repos/example/service/pulls/42":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "open", "head": map[string]string{"ref": "feature", "sha": "deadbeef"}, "base": map[string]string{"ref": "main"}})
		case "/repos/example/service/git/trees/deadbeef":
			if r.URL.Query().Get("recursive") != "1" {
				t.Error("repository tree request was not recursive")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"truncated": false, "tree": []map[string]any{{"path": ".dispatch/preview.yaml", "type": "blob", "sha": "blob-1", "size": len(document)}}})
		case "/repos/example/service/git/blobs/blob-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(document)), "size": len(document)})
		default:
			t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := githubapp.New(data, vault)
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	encryptedKey, encryptedSecret, err := manager.EncryptCredentials("github", string(privateKey), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := data.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "github", Name: "Test", WebURL: server.URL, APIURL: server.URL, AppID: 42, InstallationID: 73, EncryptedPrivateKey: encryptedKey, EncryptedWebhookSecret: encryptedSecret, State: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	a := New(data, deploy.NewService(data, &previewCleanupRecorder{cleaned: map[string]bool{}}), false, AuthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)), EventConfig{GitHubApps: manager})
	input := []byte(`{"githubAppId":"github","repository":"Example/service","pullRequestNumber":42,"path":".dispatch/preview.yaml"}`)
	response := httptest.NewRecorder()
	a.importWorkflowPreviewDocument(response, httptest.NewRequest(http.MethodPost, "/api/v1/workflow/temporary-resources/import", bytes.NewReader(input)))
	if response.Code != http.StatusOK {
		t.Fatalf("preview file import returned %d: %s", response.Code, response.Body.String())
	}
	var result struct{ Document, Path, HeadSHA string }
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Document != document || result.Path != ".dispatch/preview.yaml" || result.HeadSHA != "deadbeef" {
		t.Fatalf("import did not use the PR head file: %+v", result)
	}

	invalid := httptest.NewRecorder()
	a.importWorkflowPreviewDocument(invalid, httptest.NewRequest(http.MethodPost, "/api/v1/workflow/temporary-resources/import", bytes.NewReader([]byte(`{"githubAppId":"github","repository":"Example/service","pullRequestNumber":42,"path":"../preview.yaml"}`))))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("unsafe path returned %d: %s", invalid.Code, invalid.Body.String())
	}
}
