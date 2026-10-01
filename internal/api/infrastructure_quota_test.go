package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
	"github.com/doout/dispatch/internal/provision"
)

func TestInfrastructureQuotaPolicyAuthorization(t *testing.T) {
	a := serviceTestAPI(t)
	projects, _ := a.store.ListProjects(context.Background())
	project := projects[0]
	adapter, _ := mock.New(mock.Options{})
	endpoint := httptest.NewServer(provider.Handler(adapter, ""))
	defer endpoint.Close()
	var registered core.InfrastructureProvider
	raw := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/providers", provision.Registration{Name: "Quota test", Endpoint: endpoint.URL, Enabled: true, Capabilities: []string{provider.CapabilityInspect, provider.CapabilityCreate}}, 201)
	if err := json.Unmarshal(raw, &registered); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/projects/" + project.ID + "/infrastructure/quota"
	w := automationRequest(t, a, "secret", "GET", path, nil, 200)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("quota inventory cached")
	}
	var account core.ServiceAccount
	w = automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts", map[string]any{"name": "quota-agent"}, 201)
	json.Unmarshal(w.Body.Bytes(), &account)
	var token struct {
		Token string `json:"token"`
	}
	w = automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts/"+account.ID+"/credentials", map[string]any{"name": "token", "expiresAt": time.Now().Add(time.Hour)}, 201)
	json.Unmarshal(w.Body.Bytes(), &token)
	automationRequest(t, a, token.Token, "GET", path, nil, 403)
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: project.ID, Permissions: []core.Permission{core.PermissionInfrastructureInspect}}, 200)
	automationRequest(t, a, token.Token, "GET", path, nil, 200)
	policy := core.InfrastructureQuotaPolicy{MaxServers: 2, Providers: []core.InfrastructureProviderRule{{ProviderID: registered.ID, Regions: []string{"test"}, Sizes: []string{"small"}}}}
	automationRequest(t, a, token.Token, "PUT", path, policy, 403)
	automationRequest(t, a, "secret", "PUT", path, policy, 200)
	automationRequest(t, a, "secret", "PUT", path, policy, 409)
	policy.Revision = 1
	policy.MaxTemporaryEnvironments = 3
	automationRequest(t, a, "secret", "PUT", path, policy, 422)
	policy.MaxTemporaryLifetimeSeconds = 3600
	policy.Providers[0].AnyRegion = true
	automationRequest(t, a, "secret", "PUT", path, policy, 422)
	policy.Providers[0].AnyRegion = false
	policy.MaxServers = -1
	automationRequest(t, a, "secret", "PUT", path, policy, 200)
	violation := &core.InfrastructureQuotaViolation{Code: "quota_exceeded", Limit: "maxServers", Maximum: 2, Usage: 2, Requested: 1}
	response := httptest.NewRecorder()
	a.infrastructureProblem(response, violation)
	if response.Code != 409 {
		t.Fatal(response.Code)
	}
	var problem struct {
		Quota core.InfrastructureQuotaViolation `json:"quota"`
	}
	json.Unmarshal(response.Body.Bytes(), &problem)
	if problem.Quota.Usage != 2 || problem.Quota.Requested != 1 {
		t.Fatal("quota error omitted usage", response.Body.String())
	}
}

func TestAutomationInfrastructureAdmission(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	project := projects[0]
	now := time.Now().UTC()
	adapter, _ := mock.New(mock.Options{})
	endpoint := httptest.NewServer(provider.Handler(adapter, ""))
	defer endpoint.Close()
	var p core.InfrastructureProvider
	raw := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/providers", provision.Registration{Name: "Self service", Endpoint: endpoint.URL, Enabled: true, Capabilities: []string{provider.CapabilityInspect, provider.CapabilityCreate, provider.CapabilityDelete}}, 201)
	json.Unmarshal(raw, &p)
	if err := a.store.CreateSecret(ctx, core.Secret{ID: "approved-public-key", Name: "SSH key", Type: core.SecretTypeSSHPrivateKey, PublicValue: "ssh-ed25519 public-fixture", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var account core.ServiceAccount
	w := automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts", map[string]any{"name": "server-agent"}, 201)
	json.Unmarshal(w.Body.Bytes(), &account)
	var credential struct {
		Token string `json:"token"`
	}
	w = automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts/"+account.ID+"/credentials", map[string]any{"name": "token", "expiresAt": now.Add(time.Hour)}, 201)
	json.Unmarshal(w.Body.Bytes(), &credential)
	grant := core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: project.ID, Permissions: []core.Permission{core.PermissionProjectView, core.PermissionInfrastructureInspect, core.PermissionInfrastructureCreate}}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	input := provision.CreateInput{ProjectID: project.ID, ProviderID: p.ID, Name: "Agent server", Region: "mock-region", Size: "mock-small", Image: "mock-linux", Network: "mock-private", SSHKeySecretID: "approved-public-key", Config: map[string]any{}}
	// Neither a permission nor a quota can assign a global provider or key.
	automationRequest(t, a, credential.Token, "POST", "/api/v1/infrastructure/servers/review", input, 403)
	for kind, id := range map[string]string{"provider": p.ID, "ssh_key": "approved-public-key"} {
		automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+project.ID, map[string]any{"kind": kind, "resourceId": id}, 200)
	}
	w = automationRequest(t, a, credential.Token, "POST", "/api/v1/infrastructure/servers/review", input, 201)
	var review core.InfrastructureReview
	json.Unmarshal(w.Body.Bytes(), &review)
	acceptance := provision.Acceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Name, RequestKey: "agent-create-key"}
	// No provider create occurs before the accepted operation has a reservation.
	automationRequest(t, a, credential.Token, "POST", "/api/v1/infrastructure/servers", acceptance, 409)
	policy := core.InfrastructureQuotaPolicy{MaxServers: 1, Providers: []core.InfrastructureProviderRule{{ProviderID: p.ID, Regions: []string{input.Region}, Sizes: []string{input.Size}}}}
	automationRequest(t, a, "secret", "PUT", "/api/v1/projects/"+project.ID+"/infrastructure/quota", policy, 200)
	// A previously prepared review rechecks current grants at acceptance.
	automationRequest(t, a, "secret", "DELETE", "/api/v1/infrastructure/grants/service_account/"+account.ID+"/"+project.ID, nil, 204)
	automationRequest(t, a, credential.Token, "POST", "/api/v1/infrastructure/servers", acceptance, 403)
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	w = automationRequest(t, a, credential.Token, "POST", "/api/v1/infrastructure/servers", acceptance, 202)
	var accepted provision.Accepted
	json.Unmarshal(w.Body.Bytes(), &accepted)
	repeated := automationRequest(t, a, credential.Token, "POST", "/api/v1/infrastructure/servers", acceptance, 202)
	var replay provision.Accepted
	json.Unmarshal(repeated.Body.Bytes(), &replay)
	if replay.Operation.ID != accepted.Operation.ID {
		t.Fatal("self-service replay duplicated resource")
	}
	automationRequest(t, a, credential.Token, "POST", "/api/v1/infrastructure/servers/"+accepted.Server.ID+"/delete-review", nil, 403)
	automationRequest(t, a, credential.Token, "POST", "/api/v1/infrastructure/servers/"+accepted.Server.ID+"/delete", map[string]any{"confirmName": accepted.Server.Name}, 403)
	input.Name = "Another server"
	w = automationRequest(t, a, credential.Token, "POST", "/api/v1/infrastructure/servers/review", input, 201)
	json.Unmarshal(w.Body.Bytes(), &review)
	acceptance = provision.Acceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Name, RequestKey: "another-agent-create"}
	w = automationRequest(t, a, credential.Token, "POST", "/api/v1/infrastructure/servers", acceptance, 409)
	var denied struct {
		Quota core.InfrastructureQuotaViolation `json:"quota"`
	}
	json.Unmarshal(w.Body.Bytes(), &denied)
	if denied.Quota.Usage != 1 || denied.Quota.Maximum != 1 {
		t.Fatal("accepted intent not counted", w.Body.String())
	}
	inventory := automationRequest(t, a, credential.Token, "GET", "/api/v1/infrastructure/servers", nil, 200)
	if !strings.Contains(inventory.Body.String(), accepted.Server.ID) || strings.Contains(inventory.Body.String(), "Another server") {
		t.Fatal("inventory includes rejected allocation")
	}
	// Global provider configuration secrets remain owner-only even with creation.
	input.SecretRefs = map[string]string{"testLabel": "approved-public-key"}
	automationRequest(t, a, credential.Token, "POST", "/api/v1/infrastructure/servers/review", input, 403)
	other := core.Project{ID: "quota-other-project", Name: "Other", CreatedAt: now}
	if err := a.store.CreateProject(ctx, other); err != nil {
		t.Fatal(err)
	}
	input.SecretRefs = nil
	input.ProjectID = other.ID
	automationRequest(t, a, credential.Token, "POST", "/api/v1/infrastructure/servers/review", input, 403)
}
