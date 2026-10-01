package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
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
