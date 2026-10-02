package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/bootstrap"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/remoteruntime"
	"golang.org/x/crypto/ssh"
)

func TestTargetBootstrapAPIReviewApprovalAndSecretBoundaries(t *testing.T) {
	a := serviceTestAPI(t)
	a.auth.PublicURL = "https://dispatch.example.com"
	root := t.TempDir()
	t.Setenv("DISPATCH_EDGE_BINARY_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "linux-amd64"), []byte("reviewed agent artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	key, _ := ssh.NewPublicKey(ed25519.NewKeyFromSeed(make([]byte, 32)).Public())
	plan := core.TargetBootstrapPlan{TargetName: "Recovered target", Method: "ssh", Platform: "linux-amd64", ImageFamily: "existing-systemd", SSHHost: "192.0.2.10", SSHUser: "root", SSHVerified: true, SSHHostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}
	raw := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/bootstrap/review", map[string]any{"plan": plan, "credentials": map[string]string{"password": "private-install-password"}}, 201)
	if strings.Contains(string(raw), "private-install-password") || strings.Contains(string(raw), "encryptedInput") || strings.Contains(string(raw), "claimToken") {
		t.Fatal("private input escaped review")
	}
	var item core.TargetBootstrap
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatal(err)
	}
	m := a.bootstrapManager()
	calls := 0
	m.SSHInstall = func(context.Context, core.TargetBootstrap, bootstrap.SSHCredentials, string) error {
		calls++
		return nil
	}
	serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/bootstrap/"+item.ID+"/accept", map[string]string{"digest": item.Digest, "confirmName": "wrong target"}, 403)
	if _, err := m.InstallSSH(context.Background(), item.ID, item.Digest); err == nil || calls != 0 {
		t.Fatal("unapproved installer executed", err)
	}
	serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/bootstrap/"+item.ID+"/accept", map[string]string{"digest": item.Digest, "confirmName": item.Plan.TargetName}, 202)
	if _, err := m.InstallSSH(context.Background(), item.ID, item.Digest); err != nil || calls != 1 {
		t.Fatal("approved installer failed", err)
	}
	for _, path := range []string{"/api/v1/infrastructure/bootstrap", "/api/v1/infrastructure/bootstrap/" + item.ID} {
		raw = serviceRequestTest(t, a, "GET", path, nil, 200)
		if strings.Contains(string(raw), "private-install-password") || strings.Contains(string(raw), "encryptedInput") || strings.Contains(string(raw), "claimToken") {
			t.Fatal("private input escaped status")
		}
	}
	saved, err := m.Store.GetTargetBootstrap(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := m.Vault.Decrypt("target-bootstrap:"+item.ID, saved.EncryptedInput)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		ClaimToken string `json:"claimToken"`
	}
	if err = json.Unmarshal(plaintext, &input); err != nil {
		t.Fatal(err)
	}
	request := func(path, token string, want int) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, bytes.NewBufferString(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		a.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("claim %d %s", rr.Code, rr.Body.String())
		}
		if rr.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("claim can be cached")
		}
		return rr
	}
	path := "/api/v1/bootstrap/claims/" + item.ID
	request(path, "wrong-claim", 401)
	request("/api/v1/bootstrap/claims/foreign", input.ClaimToken, 401)
	first := request(path, input.ClaimToken, 200)
	retry := request(path, input.ClaimToken, 200)
	if first.Body.String() != retry.Body.String() {
		t.Fatal("lost claim response rotated token")
	}
	var claim bootstrap.Claim
	if err = json.Unmarshal(first.Body.Bytes(), &claim); err != nil || claim.NodeID != item.NodeID || claim.Token == "" {
		t.Fatal(claim, err)
	}
	artifact := httptest.NewRecorder()
	a.ServeHTTP(artifact, httptest.NewRequest("GET", "/edge/artifacts/"+item.Plan.ArtifactSHA256+"/linux-amd64", nil))
	if artifact.Code != 200 || artifact.Body.String() != "reviewed agent artifact" {
		t.Fatal("pinned artifact unavailable", artifact.Code)
	}
	changed := httptest.NewRecorder()
	a.ServeHTTP(changed, httptest.NewRequest("GET", "/edge/artifacts/"+strings.Repeat("0", 64)+"/linux-amd64", nil))
	if changed.Code != 404 {
		t.Fatal("changed artifact path served bytes")
	}
}

func TestRuntimeReadinessRecordsOnlyAuthenticatedArtifactEvidence(t *testing.T) {
	a, node, session, _ := runtimeAPIFixture(t)
	path := "/api/v1/edge/nodes/" + node.ID + "/runtime/jobs/next"
	request := func(artifact, token string, want int) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Dispatch-Runtime-Version", remoteruntime.APIVersion)
		req.Header.Set("X-Dispatch-Runtime-Capabilities", "deploy")
		req.Header.Set("X-Dispatch-Agent-Artifact", artifact)
		rr := httptest.NewRecorder()
		a.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("runtime evidence %d %s", rr.Code, rr.Body.String())
		}
	}
	digest := strings.Repeat("a", 64)
	request(digest, "untrusted", 401)
	request("invalid", session.Token, 422)
	stored, _ := a.store.GetPrivateNetwork(context.Background(), node.ID)
	if stored.Details["agentArtifactSHA256"] != "" {
		t.Fatal("unverified evidence persisted")
	}
	request(digest, session.Token, 204)
	stored, _ = a.store.GetPrivateNetwork(context.Background(), node.ID)
	if stored.Details["agentArtifactSHA256"] != digest || stored.Details["runtimeCheckedAt"] == "" {
		t.Fatal("authenticated evidence missing")
	}
}
