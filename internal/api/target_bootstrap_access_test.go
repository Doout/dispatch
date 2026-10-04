package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/bootstrap"
	"github.com/doout/dispatch/internal/core"
	"golang.org/x/crypto/ssh"
)

func TestTargetBootstrapScopedInspectionAndOriginalRetry(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	a.auth.PublicURL = "https://dispatch.example.com"
	root := t.TempDir()
	t.Setenv("DISPATCH_EDGE_BINARY_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "linux-amd64"), []byte("reviewed agent artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	projects, err := a.store.ListProjects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatal("project fixture", err)
	}
	project := projects[0]
	target := core.Server{ID: "bootstrap-access-target", ProjectID: project.ID, Name: "Recovery target", Address: "192.0.2.10", Runtime: core.ServerRuntimeDocker, AgentNodeID: "node-bootstrap-access-target", State: "ready", CreatedAt: time.Now()}
	if err = a.store.CreateServer(ctx, target); err != nil {
		t.Fatal(err)
	}
	key, _ := ssh.NewPublicKey(ed25519.NewKeyFromSeed(make([]byte, 32)).Public())
	plan := core.TargetBootstrapPlan{Method: "ssh", Platform: "linux-amd64", ImageFamily: "existing-systemd", SSHHost: target.Address, SSHUser: "root", SSHVerified: true, SSHHostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}
	raw := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/bootstrap/review", map[string]any{"serverId": target.ID, "plan": plan, "credentials": map[string]string{"password": "private-bootstrap-password"}}, 201)
	var item core.TargetBootstrap
	if err = json.Unmarshal(raw, &item); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/infrastructure/bootstrap/" + item.ID
	serviceRequestTest(t, a, "POST", path+"/accept", map[string]string{"digest": item.Digest, "confirmName": target.Name}, 202)
	m := a.bootstrapManager()
	calls := 0
	m.SSHInstall = func(_ context.Context, saved core.TargetBootstrap, credentials bootstrap.SSHCredentials, _ string) error {
		calls++
		if saved.ID != item.ID || credentials.Password != "private-bootstrap-password" {
			t.Error("retry changed the accepted installation")
		}
		return errors.New("interrupted installation fixture")
	}
	if _, err = m.InstallSSH(ctx, item.ID, item.Digest); err == nil {
		t.Fatal("interruption fixture unexpectedly succeeded")
	}
	var account core.ServiceAccount
	w := automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts", map[string]any{"name": "bootstrap-recovery"}, 201)
	if err = json.Unmarshal(w.Body.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	var issued struct {
		Token string `json:"token"`
	}
	w = automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts/"+account.ID+"/credentials", map[string]any{"name": "recovery", "expiresAt": time.Now().Add(time.Hour)}, 201)
	if err = json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	before, _ := m.Store.GetTargetBootstrap(ctx, item.ID)
	member := core.User{ID: "bootstrap-member", Username: "bootstrap-member", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = a.store.CreateUser(ctx, member); err != nil {
		t.Fatal(err)
	}
	memberToken, err := a.createSession(ctx, member.ID, identityForUser(member))
	if err != nil {
		t.Fatal(err)
	}
	for _, impersonating := range []bool{false, true} {
		for _, check := range []struct {
			method, url string
			status      int
		}{
			{"GET", path, 403},
			{"GET", "/api/v1/infrastructure/bootstrap", 200},
			{"POST", path + "/retry", 403},
		} {
			request := httptest.NewRequest(check.method, check.url, strings.NewReader(`{"digest":"`+item.Digest+`"}`))
			request.Header.Set("Authorization", "Bearer "+memberToken)
			if impersonating {
				request.Header.Set("Authorization", "Bearer secret")
				request.Header.Set(impersonateUserHeader, member.ID)
			}
			response := httptest.NewRecorder()
			a.ServeHTTP(response, request)
			if response.Code != check.status || check.url == "/api/v1/infrastructure/bootstrap" && strings.TrimSpace(response.Body.String()) != "[]" {
				t.Fatalf("member installation access (impersonating %t): %s %s: %d %s", impersonating, check.method, check.url, response.Code, response.Body.String())
			}
		}
	}
	automationRequest(t, a, issued.Token, "GET", path, nil, 403)
	w = automationRequest(t, a, issued.Token, "GET", "/api/v1/infrastructure/bootstrap", nil, 200)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal("ungranted inventory exposed installation", w.Body.String())
	}
	after, _ := m.Store.GetTargetBootstrap(ctx, item.ID)
	if after.Revision != before.Revision || calls != 1 {
		t.Fatal("denied inspection refreshed or executed an installation")
	}
	grant := core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: project.ID, Permissions: []core.Permission{core.PermissionInfrastructureInspect}}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	w = automationRequest(t, a, issued.Token, "GET", path, nil, 200)
	if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "private-bootstrap-password") || strings.Contains(w.Body.String(), "encryptedInput") || strings.Contains(w.Body.String(), "claimToken") {
		t.Fatal("private installation data escaped", w.Body.String())
	}
	w = automationRequest(t, a, issued.Token, "GET", "/api/v1/infrastructure/bootstrap?projectId="+project.ID+"&serverId="+target.ID, nil, 200)
	var items []core.TargetBootstrap
	if json.Unmarshal(w.Body.Bytes(), &items) != nil || len(items) != 1 || items[0].ID != item.ID {
		t.Fatal("lost scoped installation inventory", w.Body.String())
	}
	automationRequest(t, a, issued.Token, "POST", path+"/accept", map[string]string{"digest": item.Digest, "confirmName": target.Name}, 403)
	automationRequest(t, a, issued.Token, "POST", path+"/retry", map[string]string{"digest": item.Digest}, 403)
	grant.Permissions = append(grant.Permissions, core.PermissionInfrastructureModify)
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	automationRequest(t, a, issued.Token, "POST", path+"/retry", map[string]string{"digest": "changed-digest"}, 409)
	w = automationRequest(t, a, issued.Token, "POST", path+"/retry", map[string]string{"digest": item.Digest}, 202)
	var retried core.TargetBootstrap
	if json.Unmarshal(w.Body.Bytes(), &retried) != nil || retried.ID != item.ID || retried.Digest != item.Digest || retried.State != "accepted" {
		t.Fatal("retry changed installation identity", w.Body.String())
	}
	if _, err = m.InstallSSH(ctx, item.ID, item.Digest); err == nil || calls != 2 {
		t.Fatal("original accepted installer was not retried", calls, err)
	}
	before, _ = m.Store.GetTargetBootstrap(ctx, item.ID)
	if err = m.Store.RevokeEdgeCredential(ctx, item.NodeID, time.Now()); err != nil {
		t.Fatal(err)
	}
	automationRequest(t, a, issued.Token, "POST", path+"/retry", map[string]string{"digest": item.Digest}, 409)
	after, _ = m.Store.GetTargetBootstrap(ctx, item.ID)
	if after.Revision != before.Revision || calls != 2 {
		t.Fatal("revoked enrollment accepted installation recovery")
	}
	automationRequest(t, a, "secret", "DELETE", "/api/v1/infrastructure/grants/"+core.PrincipalServiceAccount+"/"+account.ID+"/"+project.ID, nil, 204)
	automationRequest(t, a, issued.Token, "GET", path, nil, 403)
	automationRequest(t, a, issued.Token, "POST", path+"/retry", map[string]string{"digest": item.Digest}, 403)
}
