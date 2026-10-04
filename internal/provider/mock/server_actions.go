package mock

import (
	"context"
	"encoding/json"

	"github.com/doout/dispatch/internal/provider"
)

func (m *Mock) PowerServer(ctx context.Context, key, id string, in provider.PowerServerRequest) (provider.Operation, error) {
	if !provider.ValidID(key) || !provider.ValidID(id) || provider.ValidatePowerRequest(in) != nil {
		return provider.Operation{}, provider.NewProblem(422, "Power request invalid", "Supply a supported action and identities.")
	}
	raw, _ := json.Marshal(in)
	keyID, requestDigest := digest(key), digest("power:"+id+":"+string(raw))
	return m.change(ctx, func(state *savedState) (provider.Operation, error) {
		if prior, ok := state.Keys[keyID]; ok {
			if prior.Digest != requestDigest {
				return provider.Operation{}, conflict()
			}
			return state.Operations[prior.OperationID].Operation, nil
		}
		s, ok := state.Servers[id]
		if !ok {
			return provider.Operation{}, provider.NewProblem(404, "Server absent", "The selected machine is absent.")
		}
		if s.State != "ready" || provider.ServerIdentityDigest(s) != in.ExpectedIdentity || s.PowerState == provider.PowerTransitioning || in.Action == "reboot" && s.PowerState != provider.PowerRunning {
			return provider.Operation{}, provider.NewProblem(409, "Machine state changed", "Inspect the selected machine before submitting this action.")
		}
		op := provider.Operation{ID: "mock-op-" + keyID[:24], ResourceID: id, State: provider.StatePending}
		s.PowerState = provider.PowerTransitioning
		state.Servers[id] = s
		state.Operations[op.ID] = savedOperation{Operation: op, Remaining: m.options.Polls, Kind: "power", Action: in.Action, Fail: m.options.FailPower}
		state.Keys[keyID] = savedKey{Digest: requestDigest, OperationID: op.ID}
		return op, nil
	})
}
func (m *Mock) PromoteServer(ctx context.Context, key, id string, in provider.PromoteServerRequest) (provider.Operation, error) {
	if !provider.ValidID(key) || !provider.ValidID(id) || provider.ValidatePromotionRequest(in) != nil {
		return provider.Operation{}, provider.NewProblem(422, "Promotion request invalid", "Supply a network and identities.")
	}
	raw, _ := json.Marshal(in)
	keyID, requestDigest := digest(key), digest("promote:"+id+":"+string(raw))
	return m.change(ctx, func(state *savedState) (provider.Operation, error) {
		if prior, ok := state.Keys[keyID]; ok {
			if prior.Digest != requestDigest {
				return provider.Operation{}, conflict()
			}
			return state.Operations[prior.OperationID].Operation, nil
		}
		s, ok := state.Servers[id]
		if !ok {
			return provider.Operation{}, provider.NewProblem(404, "Server absent", "The selected clone is absent.")
		}
		if in.Network != "mock-private" || s.Network != "mock-isolated" || s.State != "ready" || s.PowerState != provider.PowerRunning || s.Restore == nil || !s.Restore.SafeClone() || s.Promotion != nil || provider.ServerIdentityDigest(s) != in.ExpectedIdentity {
			return provider.Operation{}, provider.NewProblem(409, "Clone state changed", "Choose an isolated fresh clone and advertised destination network.")
		}
		for _, operation := range state.Operations {
			if operation.Operation.ResourceID == id && !provider.Terminal(operation.Operation) {
				return provider.Operation{}, provider.NewProblem(409, "Clone operation active", "Finish its existing operation first.")
			}
		}
		op := provider.Operation{ID: "mock-op-" + keyID[:24], ResourceID: id, State: provider.StatePending}
		state.Operations[op.ID] = savedOperation{Operation: op, Remaining: m.options.Polls, Kind: "promote", Network: in.Network, Identity: in.ExpectedIdentity, Fail: m.options.FailPromotion}
		state.Keys[keyID] = savedKey{Digest: requestDigest, OperationID: op.ID}
		return op, nil
	})
}
