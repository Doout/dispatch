package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"golang.org/x/crypto/ssh"
)

func TestBuilderHostScanReturnsPublicKeyAndFingerprint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		config := &ssh.ServerConfig{NoClientAuth: true}
		config.AddHostKey(signer)
		_, _, _, _ = ssh.NewServerConn(conn, config)
	}()
	fingerprint, key, err := scanSSHHost(context.Background(), listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint != ssh.FingerprintSHA256(signer.PublicKey()) || key != string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(signer.PublicKey()))) {
		t.Fatalf("unexpected host identity: %s %s", fingerprint, key)
	}
	<-done
}

func TestDockerBuilderServerAndCredentialLifecycle(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, false, EventConfig{Vault: vault})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/secrets", bytes.NewBufferString(`{"name":"Builder key","type":"ssh_private_key","environmentVariable":"BUILDER_KEY","generate":true}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", response.Code, response.Body.String())
	}
	var secret core.Secret
	if err := json.NewDecoder(response.Body).Decode(&secret); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"name": "builder-one", "runtime": "builder", "address": "ssh://build@example.com", "builder": map[string]any{"sshSecretId": secret.ID, "hostKey": secret.PublicValue, "maxConcurrent": 2}})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(input)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create builder: %d %s", response.Code, response.Body.String())
	}
	var builder core.Server
	if err := json.NewDecoder(response.Body).Decode(&builder); err != nil {
		t.Fatal(err)
	}
	if builder.Runtime != core.ServerRuntimeBuilder || builder.Builder == nil || builder.Builder.MaxConcurrent != 2 || core.IsDeploymentRuntime(builder.Runtime) {
		t.Fatalf("wrong builder: %#v", builder)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/secrets/"+secret.ID, nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("in-use key was removed: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/servers", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"maxConcurrent":2`)) {
		t.Fatalf("builder did not persist: %d %s", response.Code, response.Body.String())
	}
}
