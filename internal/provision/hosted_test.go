package provision

import (
	"context"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

func TestHostedProviderRequiresTenantRelayBeforeAnyNetworkAccess(t *testing.T) {
	manager := Manager{RequireRelay: true}
	if _, err := manager.Register(context.Background(), Registration{Name: "Unsafe", Endpoint: "http://127.0.0.1:9000"}); err == nil || !strings.Contains(err.Error(), "relay") {
		t.Fatal("controller endpoint accepted", err)
	}
	if _, _, err := manager.client(context.Background(), core.InfrastructureProvider{Endpoint: "https://127.0.0.1", CredentialSecretID: "unavailable"}); err == nil || !strings.Contains(err.Error(), "relay") {
		t.Fatal("stored provider bypassed hosted relay", err)
	}
}
