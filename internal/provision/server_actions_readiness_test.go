package provision

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
)

func TestSettledMachineActionReadinessUsesCurrentEvidence(t *testing.T) {
	for _, test := range []struct {
		name, action, state, power, runtime, snapshot, promotion, want string
	}{
		{"failed running action", "server.stop", "failed", "running", "ready", "", "", "ready"},
		{"cancelled running action", "server.reboot", "cancelled", "running", "ready", "", "", "ready"},
		{"failed stopped action", "server.start", "failed", "stopped", "waiting", "", "", "stopped"},
		{"failed isolated promotion", "server.promote", "failed", "running", "verified-isolated", "snapshot", "isolated", "verified-isolated"},
		{"unknown action", "server.stop", "unknown", "running", "ready", "", "", "unknown"},
		{"paused action", "server.stop", "paused", "running", "ready", "", "", "paused"},
		{"pending action", "server.stop", "pending", "running", "ready", "", "", "waiting"},
		{"failed allocation", "create", "failed", "running", "ready", "", "", "failed"},
		{"cancelled allocation", "restore", "cancelled", "running", "ready", "snapshot", "isolated", "cancelled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := ServerReadiness{
				ManagedServer:   core.ManagedServer{AllocationState: "allocated", EnrollmentState: "enrolled", PowerState: test.power, RuntimeState: test.runtime, SourceSnapshotID: test.snapshot, PromotionState: test.promotion},
				LatestOperation: &core.InfrastructureOperation{Action: test.action, State: test.state},
			}
			if got := s.waitState(time.Now()); got != test.want {
				t.Fatalf("readiness = %q, want %q", got, test.want)
			}
		})
	}
}

func TestManagedPowerInspectionKeepsConfirmationRevisionStable(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	_, accepted := f.accept(t)
	f.finish(t, accepted.Operation.ID)
	enrollMachine(t, f, accepted.Server.ID)
	baseline, err := f.m.GetManaged(ctx, accepted.Server.ID)
	if err != nil || !baseline.Deployable {
		t.Fatal("ready fixture", baseline, err)
	}
	for range 3 {
		f.now = f.now.Add(time.Second)
		status, err := f.m.GetManaged(ctx, baseline.ID)
		if err != nil || status.Revision != baseline.Revision || !status.PowerCheckedAt.Equal(baseline.PowerCheckedAt) || !status.Deployable {
			t.Fatal("unchanged inspection invalidated mutation confirmation", status, err)
		}
	}
	power, network := provider.PowerStopped, baseline.Network
	f.m.HTTPClient = &http.Client{Transport: upgradeTransport(func(r *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(r)
		if err == nil && r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/servers/") && response.StatusCode == 200 {
			var resource provider.Server
			decodeErr := json.NewDecoder(response.Body).Decode(&resource)
			response.Body.Close()
			if decodeErr != nil {
				return nil, decodeErr
			}
			resource.PowerState, resource.Network = power, network
			raw, _ := json.Marshal(resource)
			response.Body = readCloser{strings.NewReader(string(raw))}
		}
		return response, err
	})}
	f.now = f.now.Add(time.Second)
	stopped, err := f.m.GetManaged(ctx, baseline.ID)
	if err != nil || stopped.Revision <= baseline.Revision || stopped.WaitState != "stopped" || stopped.Deployable || !stopped.RuntimeReadyAfter.Equal(f.now) {
		t.Fatal("real power change did not fence readiness", stopped, err)
	}
	network = "mock-other-network"
	f.now = f.now.Add(time.Second)
	moved, err := f.m.GetManaged(ctx, baseline.ID)
	if err != nil || moved.Revision <= stopped.Revision || moved.Network != network || !moved.RuntimeReadyAfter.Equal(f.now) {
		t.Fatal("real network change was not persisted", moved, err)
	}
	f.now = f.now.Add(time.Second)
	again, err := f.m.GetManaged(ctx, baseline.ID)
	if err != nil || again.Revision != moved.Revision || !again.PowerCheckedAt.Equal(moved.PowerCheckedAt) {
		t.Fatal("stable changed state kept advancing revision", again, err)
	}
}
