package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
)

// Wrapping only the original interface prevents the mock's optional methods
// from making this a current adapter by accident.
type legacyProvider struct{ provider.Provider }

func (p legacyProvider) Manifest(context.Context) (provider.Manifest, error) {
	return provider.Manifest{APIVersion: "dispatch.provider/v1", Name: "dispatch-mock", DisplayName: "Legacy mock", Version: "1.0.0", Capabilities: []string{"server.create", "server.inspect", "server.delete", "server.ownership"}, ConfigurationSchema: json.RawMessage(`{"type":"object"}`)}, nil
}
func (p legacyProvider) Server(ctx context.Context, id string) (provider.Server, error) {
	s, err := p.Provider.Server(ctx, id)
	s.PowerState, s.PowerOperationID, s.Network, s.Promotion = "", "", "", nil
	return s, err
}

func TestLegacyV1ProviderRemainsValidWithoutMachineActions(t *testing.T) {
	adapter, err := mock.New(mock.Options{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(provider.Handler(legacyProvider{adapter}, ""))
	defer server.Close()
	client, err := provider.NewClient(server.URL, "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	report, err := provider.RunConformance(context.Background(), client, provider.ConformanceOptions{Request: fixture(), PollInterval: time.Millisecond, Timeout: time.Second})
	if err != nil || len(report.Checks) != 10 {
		t.Fatal("original provider contract regressed", report, err)
	}
	_, err = client.PowerServer(context.Background(), "original-key", "legacy-machine", provider.PowerServerRequest{Action: "stop", ExpectedIdentity: strings.Repeat("a", 64)})
	var problem *provider.Problem
	if !errors.As(err, &problem) || problem.Status != 422 {
		t.Fatal("legacy provider exposed an unimplemented power action", err)
	}
	_, err = client.PromoteServer(context.Background(), "original-key", "legacy-machine", provider.PromoteServerRequest{Network: "workload-network", ExpectedIdentity: strings.Repeat("a", 64)})
	if !errors.As(err, &problem) || problem.Status != 422 {
		t.Fatal("legacy provider exposed an unimplemented promotion action", err)
	}
}
