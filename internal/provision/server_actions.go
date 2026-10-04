package provision

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/store"
)

type PowerInput struct {
	Action   string `json:"action"`
	Revision int64  `json:"revision"`
}
type PromotionInput struct {
	Network     string `json:"network"`
	Revision    int64  `json:"revision"`
	ConfirmName string `json:"confirmName"`
}
type CloneInspection struct {
	Server               ServerReadiness `json:"server"`
	Resource             provider.Server `json:"resource"`
	Verified             bool            `json:"verified"`
	ApplicationIntegrity string          `json:"applicationIntegrity"`
}
type machineActionRequest struct {
	Power                *provider.PowerServerRequest   `json:"power,omitempty"`
	Promotion            *provider.PromoteServerRequest `json:"promotion,omitempty"`
	NodeID               string                         `json:"nodeId"`
	CredentialGeneration int64                          `json:"credentialGeneration,omitempty"`
	CredentialPublicKey  string                         `json:"credentialPublicKey,omitempty"`
	PriorPowerState      string                         `json:"priorPowerState"`
	PriorPromotionState  string                         `json:"priorPromotionState"`
}

func (m *Manager) actionStore() (store.InfrastructureActionStore, error) {
	s, ok := m.Store.(store.InfrastructureActionStore)
	if !ok {
		return nil, errors.New("durable machine action storage is unavailable")
	}
	return s, nil
}
func (m *Manager) InspectClone(ctx context.Context, id string) (CloneInspection, error) {
	var out CloneInspection
	out.ApplicationIntegrity = "unverified"
	readiness, err := m.GetManaged(ctx, id)
	if err != nil {
		return out, err
	}
	out.Server = readiness
	if readiness.SourceSnapshotID == "" {
		return out, errors.New("this machine is not a snapshot clone")
	}
	data, err := m.lifecycle()
	if err != nil {
		return out, err
	}
	review, err := data.GetInfrastructureReview(ctx, readiness.ReviewID)
	if err != nil {
		return out, err
	}
	client, _, err := m.Adapter(ctx, readiness.ProviderID, provider.CapabilityInspect, "")
	if err != nil {
		return out, err
	}
	resource, err := client.Server(ctx, readiness.ResourceID)
	if err != nil {
		return out, err
	}
	if ownedResource(review, resource) != nil || !m.safeEvidence(review, resource) {
		return out, errors.New("clone ownership or public evidence changed")
	}
	out.Resource = resource
	out.Verified = resource.State == "ready" && m.verifyReviewedClone(review, resource) == nil
	return out, nil
}
func (m *Manager) Power(ctx context.Context, id, actor, key string, in PowerInput) (Accepted, error) {
	if provider.PowerCapability(in.Action) == "" || in.Revision < 1 {
		return Accepted{}, errors.New("choose start, stop or reboot and a current server revision")
	}
	return m.acceptMachineAction(ctx, id, actor, key, "server."+in.Action, in, in.Revision, "")
}
func (m *Manager) Promote(ctx context.Context, id, actor, key string, in PromotionInput) (Accepted, error) {
	if !provider.ValidID(in.Network) || in.Revision < 1 || in.ConfirmName == "" {
		return Accepted{}, errors.New("choose an explicit destination network, revision and clone name")
	}
	return m.acceptMachineAction(ctx, id, actor, key, "server.promote", in, in.Revision, in.ConfirmName)
}
func (m *Manager) acceptMachineAction(ctx context.Context, id, actor, key, action string, input any, revision int64, name string) (Accepted, error) {
	var out Accepted
	data, err := m.lifecycle()
	if err != nil {
		return out, err
	}
	actions, err := m.actionStore()
	if err != nil {
		return out, err
	}
	server, err := data.GetManagedServer(ctx, id)
	if err != nil {
		return out, err
	}
	if err = m.authorize(ctx, server.ProjectID, server.ProviderID, "infrastructure.modify"); err != nil {
		return out, err
	}
	if action == "server.promote" {
		if err = m.authorize(ctx, server.ProjectID, server.ProviderID, "infrastructure.restore"); err != nil {
			return out, err
		}
	}
	opID := ""
	if claim, ok := core.MutationAcceptanceFromContext(ctx); ok {
		opID = claim.OperationID
	} else {
		opID, err = operationID(server.ProviderID, actor, key, action)
		if err != nil {
			return out, err
		}
	}
	raw, _ := json.Marshal(input)
	requestDigest := digest(id + "\x00" + action + "\x00" + string(raw))
	if old, e := data.GetInfrastructureOperation(ctx, opID); e == nil {
		if old.ServerID != id || old.ActorID != actor || old.Action != action || old.RequestDigest != requestDigest {
			return out, store.ErrInfrastructureChanged
		}
		return Accepted{server, old}, nil
	} else if !errors.Is(e, store.ErrNotFound) {
		return out, e
	}
	if server.Revision != revision || server.AllocationState != "allocated" || server.ResourceID == "" {
		return out, store.ErrInfrastructureChanged
	}
	capability := action
	client, p, err := m.Adapter(ctx, server.ProviderID, capability, "")
	if err != nil {
		return out, err
	}
	if !slices.Contains(p.Capabilities, provider.CapabilityInspect) {
		return out, errors.New("machine actions require approved provider inspection")
	}
	review, err := data.GetInfrastructureReview(ctx, server.ReviewID)
	if err != nil {
		return out, err
	}
	resource, err := client.Server(ctx, server.ResourceID)
	if err != nil {
		return out, err
	}
	if resource.State != "ready" || ownedResource(review, resource) != nil || !m.safeEvidence(review, resource) {
		return out, errors.New("machine ownership or public evidence changed")
	}
	request := machineActionRequest{NodeID: server.NodeID, PriorPowerState: resource.PowerState, PriorPromotionState: server.PromotionState}
	if action == "server.promote" {
		if server.Name != name || server.SourceSnapshotID == "" || server.PromotionState != "isolated" || resource.Promotion != nil || resource.PowerState != provider.PowerRunning || m.verifyReviewedClone(review, resource) != nil {
			return out, store.ErrInfrastructureChanged
		}
		enrollment, ok := m.Store.(EnrollmentStore)
		if !ok {
			return out, errors.New("clone enrollment storage is unavailable")
		}
		if err = m.refreshServerReadiness(ctx, enrollment, server); err != nil {
			return out, err
		}
		server, err = data.GetManagedServer(ctx, id)
		if err != nil {
			return out, err
		}
		if server.EnrollmentState != "enrolled" || server.RuntimeState != "verified-isolated" {
			return out, errors.New("clone requires current fresh enrollment and runtime readiness")
		}
		credential, e := enrollment.GetEdgeCredential(ctx, server.NodeID)
		if e != nil || credential.Revoked || credential.PublicKey == "" {
			return out, store.ErrInfrastructureChanged
		}
		request.CredentialGeneration = credential.Generation
		request.CredentialPublicKey = credential.PublicKey
		configured, e := m.reviewedRequest(review)
		if e != nil {
			return out, e
		}
		options, e := client.Options(ctx, provider.OptionRequest{Kind: "networks", Config: configured.ProviderConfig})
		if e != nil {
			return out, e
		}
		network := input.(PromotionInput).Network
		found := false
		for _, option := range options {
			if option.ID == network && option.Metadata["quarantine"] != true {
				found = true
			}
		}
		if !found {
			return out, errors.New("choose a currently advertised workload network")
		}
		request.Promotion = &provider.PromoteServerRequest{Network: network, ExpectedIdentity: provider.ServerIdentityDigest(resource)}
		server.PromotionState = "pending"
	} else {
		in := input.(PowerInput)
		if resource.PowerState != provider.PowerRunning && resource.PowerState != provider.PowerStopped && resource.PowerState != provider.PowerUnknown || in.Action == "reboot" && resource.PowerState != provider.PowerRunning {
			return out, errors.New("current machine power state does not permit this action")
		}
		request.Power = &provider.PowerServerRequest{Action: in.Action, ExpectedIdentity: provider.ServerIdentityDigest(resource)}
		server.PowerState = provider.PowerTransitioning
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return out, err
	}
	cipher, err := m.Vault.Encrypt("infrastructure-action:"+opID, encoded)
	if err != nil {
		return out, err
	}
	now := m.now()
	op := core.InfrastructureOperation{ID: opID, ServerID: server.ID, ProviderID: server.ProviderID, ActorID: actor, Action: action, State: "pending", Stage: "submit", ResourceID: server.ResourceID, RequestDigest: requestDigest, ExpiresAt: now.Add(30 * time.Minute), NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	saved := core.InfrastructureActionRequest{OperationID: opID, ProviderRevision: p.Revision, ManifestDigest: p.ManifestDigest, EncryptedRequest: cipher, CipherDigest: digest(cipher)}
	out.Server, out.Operation, err = actions.AcceptInfrastructureAction(ctx, server, op, saved, now, m.Admission)
	if err == nil {
		core.RecordAcceptedOperation(ctx, opID)
	}
	return out, err
}
