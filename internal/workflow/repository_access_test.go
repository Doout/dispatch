package workflow

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/repositoryproxy"
	"github.com/doout/dispatch/internal/secretvalue"
	"github.com/doout/dispatch/internal/store"
)

func TestRepositoryCloneURLUsesSavedCredentialHost(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		repository string
		want       string
	}{
		{
			name:       "SSH",
			configured: "git@example.test:platform/config.git",
			repository: "platform/service",
			want:       "git@example.test:platform/service.git",
		},
		{
			name:       "HTTPS",
			configured: "https://example.test/platform/config.git",
			repository: "platform/ui",
			want:       "https://example.test/platform/ui.git",
		},
		{
			name:       "explicit clone URL",
			configured: "git@example.test:platform/config.git",
			repository: "ssh://git@other.example.test/platform/chart.git",
			want:       "ssh://git@other.example.test/platform/chart.git",
		},
	}
	service := &Service{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := service.repositoryCloneURL(context.Background(), core.ConfigSource{Repository: test.configured}, test.repository)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("repository URL = %q, want %q", got, test.want)
			}
		})
	}
}

func TestParseRepositoryHeadIgnoresSSHMessages(t *testing.T) {
	want := "0123456789abcdef0123456789abcdef01234567"
	output := "Warning: Permanently added 'example.test' to the list of known hosts.\n" + want + "\trefs/heads/main\n"
	got, err := parseRepositoryHead(output, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("repository head = %q, want %q", got, want)
	}
}

func TestParseRepositoryHeadRejectsWrongRef(t *testing.T) {
	_, err := parseRepositoryHead("0123456789abcdef0123456789abcdef01234567\trefs/heads/other\n", "refs/heads/main")
	if err == nil {
		t.Fatal("expected the wrong ref to be rejected")
	}
}

func TestWorkflowCheckoutFetchesThroughSavedRepositoryProxy(t *testing.T) {
	runtime, _ := schedulerFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root := t.TempDir()
	repo := filepath.Join(root, "working")
	if err := os.Mkdir(filepath.Join(root, "team"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.test")
	for _, args := range [][]string{
		{"init", "--initial-branch=main", repo},
		{"-C", repo, "commit", "--allow-empty", "-m", "Proxy checkout"},
		{"clone", "--bare", repo, filepath.Join(root, "team", "source.git")},
	} {
		if err := runGit(ctx, env, args...); err != nil {
			t.Fatal(err)
		}
	}
	commit := gitOutput(t, repo, "rev-parse", "HEAD")
	execPath, err := runGitOutput("--exec-path")
	if err != nil {
		t.Fatal(err)
	}
	backend := &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "github.example.test" || r.Header.Get("Authorization") != "Bearer test-fixture" {
			t.Errorf("unexpected Git request to proxy: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer proxy.Close()
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := vault.Encrypt("secret:checkout", []byte("test-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	data := runtime.service.Store
	appID := runtime.source.GitHubAppID
	if err := data.CreateSecret(ctx, core.Secret{ID: "checkout", Name: "checkout", Type: core.SecretTypeGitHubToken, EncryptedValue: encrypted, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := data.SaveRepositoryProxy(ctx, core.RepositoryProxySettings{URL: proxy.URL}, true); err != nil {
		t.Fatal(err)
	}
	conflictingConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(conflictingConfig, []byte("[http]\n\tproxy = http://127.0.0.1:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", conflictingConfig)
	t.Setenv("no_proxy", "*")
	runtime.source.GitHubAppID, runtime.source.CredentialSecretID = "", "checkout"
	runtime.service.Secrets = secretvalue.New(data, vault)
	runtime.service.RepositoryProxy = &repositoryproxy.Service{Store: data, Vault: vault}
	runtime.service.Repositories = newRepositoryCache(t.TempDir())
	runtime.revision.Sources["code"] = core.WorkflowSourceRevision{
		Alias: "code", Repository: "http://github.example.test/team/source.git", Branch: "main", CommitSHA: commit,
	}
	defer runtime.close()
	path, err := runtime.checkout(ctx, "code")
	if err != nil {
		t.Fatal(err)
	}
	if actual := gitOutput(t, path, "rev-parse", "HEAD"); actual != commit {
		t.Fatalf("checked out %s, want %s", actual, commit)
	}

	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, token, ok := r.BasicAuth()
		if r.Host != "example.com" || !ok || user != "x-access-token" || token != "test-fixture" {
			t.Error("Git checkout did not use the App credential")
			http.Error(w, "unexpected checkout", http.StatusBadRequest)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer origin.Close()
	cert := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: origin.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSL_CAINFO", cert)
	appProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect && r.Host == "example.com:443" {
			upstream, err := net.Dial("tcp", origin.Listener.Addr().String())
			if err != nil {
				t.Error(err)
				return
			}
			defer upstream.Close()
			client, buffered, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer client.Close()
			if _, err = io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
				t.Error(err)
				return
			}
			go func() {
				_, _ = io.Copy(upstream, buffered.Reader)
				_ = upstream.(*net.TCPConn).CloseWrite()
			}()
			_, _ = io.Copy(client, upstream)
			return
		}
		switch r.URL.Path {
		case "/repos/team/source/installation":
			_, _ = fmt.Fprint(w, `{"id":2,"app_id":1}`)
		case "/app/installations/2/access_tokens":
			_, _ = fmt.Fprintf(w, `{"token":"test-fixture","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
		default:
			t.Errorf("unexpected proxy request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer appProxy.Close()
	manager := githubapp.New(data, vault)
	manager.Client = &http.Client{Transport: runtime.service.RepositoryProxy.Transport()}
	connection, err := data.GetGitHubApp(ctx, appID)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	connection.EncryptedPrivateKey, connection.EncryptedWebhookSecret, err = manager.EncryptCredentials(appID, privateKey, "webhook-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	connection.WebURL, connection.APIURL = "https://example.com", "http://github.example.test"
	connection.Proxy.URL = appProxy.URL
	if err := data.UpdateGitHubApp(ctx, connection, &store.GitHubAppProxyEdit{Address: true}); err != nil {
		t.Fatal(err)
	}
	runtime.source.GitHubAppID, runtime.source.CredentialSecretID = appID, ""
	runtime.service.GitHub = manager
	_, err = runtime.service.workerSource(ctx, runtime.source, core.WorkflowSourceRevision{Repository: "https://other.example/team/source.git"})
	if err == nil || !strings.Contains(err.Error(), "does not match GitHub App host") {
		t.Fatalf("unexpected validation result for a checkout on another host: %v", err)
	}
	runtime.revision.Sources["code"] = core.WorkflowSourceRevision{
		Alias: "code", Repository: "https://example.com/team/source.git", Branch: "main", CommitSHA: commit,
	}
	runtime.root = t.TempDir()
	delete(runtime.paths, "code")
	path, err = runtime.checkout(ctx, "code")
	if err != nil {
		t.Fatal(err)
	}
	if actual := gitOutput(t, path, "rev-parse", "HEAD"); actual != commit {
		t.Fatalf("App checkout fetched %s, want %s", actual, commit)
	}
}
