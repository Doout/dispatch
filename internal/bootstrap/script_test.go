package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestInstallerClientRejectsChangedArtifactRedirectsAndForeignIdentity(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 unavailable")
	}
	raw := []byte("approved agent bytes")
	sum := sha256.Sum256(raw)
	var mode atomic.Int32
	var forwarded atomic.Int32
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1); w.WriteHeader(500) }))
	defer redirect.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/artifact" {
			if r.Header.Get("Authorization") != "" {
				t.Error("claim sent to artifact endpoint")
			}
			if mode.Load() == 1 {
				w.Write([]byte("changed"))
				return
			}
			if mode.Load() == 2 {
				http.Redirect(w, r, redirect.URL, http.StatusTemporaryRedirect)
				return
			}
			w.Write(raw)
			return
		}
		if r.Header.Get("Authorization") != "Bearer private-bootstrap-claim" {
			t.Error("missing scoped claim")
		}
		if mode.Load() == 3 {
			http.Redirect(w, r, redirect.URL, http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state":"enroll","nodeId":"node-target","token":"fresh-one-use-token"}`))
	}))
	defer server.Close()
	root := t.TempDir()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err = os.WriteFile(filepath.Join(root, "ca.pem"), cert, 0600); err != nil {
		t.Fatal(err)
	}
	item := core.TargetBootstrap{ID: "review", NodeID: "node-target", Plan: core.TargetBootstrapPlan{ControllerURL: server.URL, ArtifactURL: server.URL + "/artifact", ArtifactSHA256: hex.EncodeToString(sum[:])}}
	script, err := renderScript(item, "private-bootstrap-claim")
	if err != nil {
		t.Fatal(err)
	}
	shell := exec.Command("sh", "-n")
	shell.Stdin = strings.NewReader(script)
	if output, e := shell.CombinedOutput(); e != nil {
		t.Fatal("generated shell invalid", e, string(output))
	}
	client := strings.Split(strings.Split(script, "<<'PYTHON'\n")[1], "\nPYTHON")[0]
	// Bound retries for negative tests; execute the same generated HTTPS client.
	client = strings.ReplaceAll(client, "range(120)", "range(1)")
	client = strings.ReplaceAll(client, "time.sleep(5)", "time.sleep(0)")
	identityPath := filepath.Join(root, "identity.json")
	client = strings.ReplaceAll(client, "/var/lib/dispatch-edge/identity.json", identityPath)
	if err = os.WriteFile(filepath.Join(root, "client.py"), []byte(client), 0600); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(map[string]any{"id": item.ID, "nodeId": item.NodeID, "plan": item.Plan, "claim": "private-bootstrap-claim"})
	if err = os.WriteFile(filepath.Join(root, "config.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(command string, wantSuccess bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, python, filepath.Join(root, "client.py"), command)
		cmd.Env = append(os.Environ(), "SSL_CERT_FILE="+filepath.Join(root, "ca.pem"))
		output, e := cmd.CombinedOutput()
		if (e == nil) != wantSuccess {
			t.Fatalf("%s success=%v: %v %s", command, wantSuccess, e, output)
		}
		if strings.Contains(string(output), "private-bootstrap-claim") || strings.Contains(string(output), "fresh-one-use-token") {
			t.Fatal("installer leaked credential")
		}
	}
	run("download", true)
	received, _ := os.ReadFile(filepath.Join(root, "agent"))
	if string(received) != string(raw) {
		t.Fatal("wrong artifact bytes")
	}
	mode.Store(1)
	run("download", false)
	mode.Store(2)
	run("download", false)
	mode.Store(0)
	run("enroll", true)
	environment, _ := os.ReadFile(filepath.Join(root, "edge.env"))
	if !strings.Contains(string(environment), "DISPATCH_EDGE_NODE_ID=node-target") || !strings.Contains(string(environment), "DISPATCH_EDGE_TOKEN=fresh-one-use-token") {
		t.Fatal("wrong intended enrollment")
	}
	mode.Store(3)
	run("enroll", false)
	if forwarded.Load() != 0 {
		t.Fatal("client followed redirect with bootstrap capability")
	}
	if err = os.WriteFile(identityPath, []byte(`{"controller":"https://another.example.com","nodeId":"source-node"}`), 0600); err != nil {
		t.Fatal(err)
	}
	run("identity", false)
}
