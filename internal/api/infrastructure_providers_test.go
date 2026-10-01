package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
	"github.com/doout/dispatch/internal/provision"
)

func TestInfrastructureProviderAPICredentialsOptionsAndDisable(t *testing.T) {
	a := serviceTestAPI(t)
	adapter, _ := mock.New(mock.Options{})
	upstream := httptest.NewServer(provider.Handler(adapter, "private-provider-token"))
	defer upstream.Close()
	var secret core.Secret
	raw := serviceRequestTest(t, a, "POST", "/api/v1/secrets", map[string]any{"name": "Provider token", "type": "api_token", "environmentVariable": "PROVIDER_TOKEN", "value": "private-provider-token"}, 201)
	if err := json.Unmarshal(raw, &secret); err != nil {
		t.Fatal(err)
	}
	in := provision.Registration{Name: "Provider", Endpoint: upstream.URL, CredentialSecretID: secret.ID, Enabled: true, Capabilities: []string{provider.CapabilityInspect, provider.CapabilityCreate, provider.CapabilityDelete}}
	raw = serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/providers", in, 201)
	var item core.InfrastructureProvider
	if err := json.Unmarshal(raw, &item); err != nil || item.State != "ready" {
		t.Fatalf("registration: %s %v", raw, err)
	}
	if strings.Contains(string(raw), "private-provider-token") {
		t.Fatal("credential exposed")
	}
	raw = serviceRequestTest(t, a, "GET", "/api/v1/infrastructure/providers", nil, 200)
	if strings.Contains(string(raw), "private-provider-token") {
		t.Fatal("credential listed")
	}
	serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/providers/"+item.ID+"/options", map[string]any{"kind": "regions", "config": map[string]any{"testLabel": "fixture"}}, 200)
	serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/providers/"+item.ID+"/options", map[string]any{"kind": "regions", "config": map[string]any{"typo": "private-provider-token"}}, 422)
	serviceRequestTest(t, a, "DELETE", "/api/v1/secrets/"+secret.ID, nil, 409)
	serviceRequestTest(t, a, "PUT", "/api/v1/secrets/"+secret.ID, map[string]any{"name": secret.Name, "type": "environment_variable", "environmentVariable": "PROVIDER_TOKEN", "value": "private-provider-token"}, 409)
	in.Revision = item.Revision
	in.Enabled = false
	raw = serviceRequestTest(t, a, "PUT", "/api/v1/infrastructure/providers/"+item.ID, in, 200)
	if err := json.Unmarshal(raw, &item); err != nil || item.State != "disabled" {
		t.Fatalf("disable: %s %v", raw, err)
	}
	if _, _, err := a.infrastructureManager().Adapter(context.Background(), item.ID, provider.CapabilityCreate, item.ManifestDigest); err == nil {
		t.Fatal("disabled provider enabled a create")
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/infrastructure/providers/"+item.ID, in, 409)
	usage := serviceRequestTest(t, a, "GET", "/api/v1/secrets/usage", nil, 200)
	if !strings.Contains(string(usage), "infrastructure_provider") {
		t.Fatal("provider credential reference absent from usage")
	}
}
