package provision

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/store"
)

func (m *Manager) reconcileMachineAction(ctx context.Context, op *core.InfrastructureOperation, resolution ...bool) error {
	inspectOnly := len(resolution) > 0 && resolution[0]
	if inspectOnly && (op.Stage != "poll" || op.ProviderOperationID == "") {
		return store.ErrInfrastructureChanged
	}
	data, err := m.lifecycle()
	if err != nil {
		return err
	}
	actions, err := m.actionStore()
	if err != nil {
		return err
	}
	server, err := data.GetManagedServer(ctx, op.ServerID)
	if err != nil {
		return err
	}
	review, err := data.GetInfrastructureReview(ctx, server.ReviewID)
	if err != nil {
		return err
	}
	var request machineActionRequest
	complete := func(state, code, message string) error {
		op.State = state
		op.ErrorCode = code
		op.Message = message
		server.Revision++
		server.UpdatedAt = m.now()
		return actions.CompleteInfrastructureAction(context.WithoutCancel(ctx), server, *op, core.EdgeCredential{NetworkID: server.NodeID, Generation: request.CredentialGeneration, PublicKey: request.CredentialPublicKey}, m.now(), m.Admission)
	}
	unknown := func(code, message string) error {
		server.RuntimeState = "waiting"
		if op.Action != "server.promote" {
			server.PowerState = provider.PowerUnknown
		}
		if op.Action == "server.promote" {
			server.PromotionState = "unknown"
		}
		return complete("unknown", code, message)
	}
	record, err := actions.GetInfrastructureActionRequest(ctx, op.ID)
	if err != nil {
		return unknown("request_unavailable", "The original encrypted machine action is unavailable.")
	}
	raw, err := m.Vault.Decrypt("infrastructure-action:"+op.ID, record.EncryptedRequest)
	if err != nil || digest(record.EncryptedRequest) != record.CipherDigest || json.Unmarshal(raw, &request) != nil {
		return unknown("request_unavailable", "The original encrypted machine action is unavailable.")
	}
	checkpoint := func() error {
		return data.CheckpointInfrastructureOperation(context.WithoutCancel(ctx), *op, true, m.now())
	}
	retry := func(e error) error {
		var problem *provider.Problem
		if errors.As(e, &problem) && problem.Status < 500 && problem.Status != http.StatusTooManyRequests {
			return unknown("provider_evidence", "The provider rejected the original machine action. Inspect its operation before recovery.")
		}
		if problem == nil && !errors.Is(e, provider.ErrTransport) && !errors.Is(e, context.Canceled) && !errors.Is(e, context.DeadlineExceeded) {
			return unknown("provider_evidence", "Provider identity or machine action evidence changed.")
		}
		op.Attempts++
		op.ErrorCode = "transport"
		op.Message = "Provider communication failed; retry retains the original machine action."
		if op.Attempts >= 8 {
			op.State = "paused"
		}
		op.NextAttemptAt = m.now().Add(time.Duration(1<<min(op.Attempts, 6)) * time.Second)
		return checkpoint()
	}
	if !inspectOnly && (!op.ExpiresAt.After(m.now()) || op.CancelRequested && op.Stage != "poll") {
		if op.Stage == "submit" {
			server.PowerState = request.PriorPowerState
			server.PromotionState = request.PriorPromotionState
			return complete("cancelled", "", "Cancelled before provider submission.")
		}
		return unknown("cancelled_unknown", "Submission may have reached the provider. The original machine action requires inspection.")
	}
	if request.NodeID != server.NodeID || op.ResourceID != server.ResourceID {
		return unknown("resource_changed", "The original machine or enrollment identity changed.")
	}
	capability := provider.CapabilityInspect
	manifest := ""
	if op.Stage == "submit" || op.Stage == "submitting" {
		capability = op.Action
		manifest = record.ManifestDigest
	}
	client, _, err := m.Adapter(ctx, server.ProviderID, capability, manifest)
	if err != nil {
		return retry(err)
	}
	if op.Stage == "submit" || op.Stage == "submitting" {
		op.Stage = "submitting"
		op.State = "running"
		if err = data.CheckpointInfrastructureOperation(ctx, *op, false, m.now()); err != nil {
			return err
		}
		latest, e := data.GetInfrastructureOperation(ctx, op.ID)
		if e != nil {
			return e
		}
		if latest.CancelRequested || !latest.ExpiresAt.After(m.now()) {
			return unknown("cancelled_unknown", "The original machine action was interrupted at submission.")
		}
		resource, e := client.Server(ctx, server.ResourceID)
		if e != nil {
			return retry(e)
		}
		if ownedResource(review, resource) != nil || !m.safeEvidence(review, resource) {
			return unknown("ownership", "The selected machine no longer matches its allocation ownership.")
		}
		var remote provider.Operation
		if request.Power != nil {
			if provider.ServerIdentityDigest(resource) != request.Power.ExpectedIdentity {
				return unknown("identity_changed", "The selected machine identity changed before its power action.")
			}
			remote, err = client.PowerServer(ctx, op.ID, server.ResourceID, *request.Power)
		} else if request.Promotion != nil {
			// A lost acknowledgement may already have released quarantine. Replay is
			// allowed only through the original provider key and immutable identity.
			if provider.ServerIdentityDigest(resource) != request.Promotion.ExpectedIdentity || m.verifyReviewedClone(review, resource) != nil {
				return unknown("clone_changed", "The original clone identity or disk evidence changed.")
			}
			credential, valid := m.currentActionEnrollment(ctx, server, request)
			if !valid || credential.Revoked {
				return unknown("enrollment_changed", "The clone enrollment changed before network release.")
			}
			remote, err = client.PromoteServer(ctx, op.ID, server.ResourceID, *request.Promotion)
		} else {
			return unknown("request_invalid", "The saved machine action has no supported request.")
		}
		if err != nil {
			return retry(err)
		}
		if remote.ResourceID != server.ResourceID || !m.safeEvidence(review, remote) {
			return unknown("resource_changed", "The provider action names another machine or private evidence.")
		}
		op.ProviderOperationID = remote.ID
		op.Stage = "poll"
		op.NextAttemptAt = m.now()
		op.Attempts = 0
		return checkpoint()
	}
	remote, err := client.Operation(ctx, op.ProviderOperationID)
	if err != nil {
		return retry(err)
	}
	if remote.ResourceID != server.ResourceID || !m.safeEvidence(review, remote) {
		return unknown("resource_changed", "The original provider operation names another machine.")
	}
	if remote.State == provider.StatePending || remote.State == provider.StateRunning {
		if inspectOnly {
			return unknown("provider_pending", "The original provider operation has not completed; inspect again later.")
		}
		op.NextAttemptAt = m.now().Add(2 * time.Second)
		return checkpoint()
	}
	if remote.State != provider.StateSucceeded {
		if !inspectOnly {
			return unknown("provider_terminal", "The provider did not confirm completion of the original machine action.")
		}
		resource, e := client.Server(ctx, server.ResourceID)
		if e != nil {
			return retry(e)
		}
		if ownedResource(review, resource) != nil || !m.safeEvidence(review, resource) {
			return unknown("ownership", "The original machine ownership changed during failure inspection.")
		}
		if request.Power != nil {
			if provider.ServerIdentityDigest(resource) != request.Power.ExpectedIdentity || (resource.PowerState != provider.PowerRunning && resource.PowerState != provider.PowerStopped) {
				return unknown("power_unconfirmed", "The failed operation lacks stable machine evidence.")
			}
			server.PowerState = resource.PowerState
			server.PowerCheckedAt = m.now()
			server.RuntimeReadyAfter = m.now()
		} else if request.Promotion != nil {
			original, e := m.reviewedRequest(review)
			if e != nil {
				return e
			}
			if resource.Promotion != nil || resource.Network != original.Network || provider.ServerIdentityDigest(resource) != request.Promotion.ExpectedIdentity || m.verifyReviewedClone(review, resource) != nil {
				return unknown("promotion_unconfirmed", "Failed promotion did not confirm the clone remains isolated.")
			}
			server.PromotionState = "isolated"
		}
		server.RuntimeState = "waiting"
		return complete("failed", "provider_terminal", "The failed original machine operation was inspected; no replacement was submitted.")
	}
	resource, err := client.Server(ctx, server.ResourceID)
	if err != nil {
		return retry(err)
	}
	if resource.State != "ready" || ownedResource(review, resource) != nil || !m.safeEvidence(review, resource) {
		return unknown("ownership", "The completed machine action lacks current allocation ownership.")
	}
	if request.Power != nil {
		if provider.VerifyPowerEvidence(resource, op.ProviderOperationID, *request.Power) != nil {
			return unknown("power_unconfirmed", "The provider did not confirm the original machine power change.")
		}
		server.PowerState = resource.PowerState
		server.PowerCheckedAt = m.now()
		server.RuntimeReadyAfter = m.now()
	}
	if request.Promotion != nil {
		if provider.VerifyPromotionEvidence(resource, op.ProviderOperationID, *request.Promotion) != nil || m.verifyReviewedClone(review, resource) != nil {
			return unknown("promotion_unconfirmed", "The provider did not confirm clone identity and network release.")
		}
		if _, valid := m.currentActionEnrollment(ctx, server, request); !valid {
			return unknown("enrollment_changed", "Network release completed but the original clone enrollment changed.")
		}
		server.PromotionState = "promoted"
		server.PromotedAt = m.now()
		server.PromotionEvidence, _ = json.Marshal(resource.Promotion)
		server.PowerState = resource.PowerState
		server.PowerCheckedAt = m.now()
		server.RuntimeReadyAfter = m.now()
	}
	server.Address = resource.Address
	server.Network = resource.Network
	server.RuntimeState = "waiting"
	return complete("succeeded", "", "")
}
func (m *Manager) currentActionEnrollment(ctx context.Context, server core.ManagedServer, request machineActionRequest) (core.EdgeCredential, bool) {
	data, ok := m.Store.(EnrollmentStore)
	if !ok {
		return core.EdgeCredential{}, false
	}
	c, err := data.GetEdgeCredential(ctx, server.NodeID)
	if err != nil || c.Revoked || c.PublicKey != request.CredentialPublicKey || c.Generation != request.CredentialGeneration {
		return c, false
	}
	return c, true
}

// Fresh inspection updates power evidence without changing an active journal's
// uncertainty or publishing an isolated clone as a workload target.
func (m *Manager) refreshMachineEvidence(ctx context.Context, server core.ManagedServer) (core.ManagedServer, error) {
	if server.AllocationState != "allocated" {
		return server, nil
	}
	data, err := m.lifecycle()
	if err != nil {
		return server, err
	}
	ops, err := data.ListInfrastructureOperations(ctx, server.ID)
	if err != nil {
		return server, err
	}
	for _, op := range ops {
		if op.State == "pending" || op.State == "running" || op.State == "paused" || op.State == "unknown" {
			return server, nil
		}
	}
	client, _, err := m.Adapter(ctx, server.ProviderID, provider.CapabilityInspect, "")
	if err != nil {
		return server, nil
	}
	resource, err := client.Server(ctx, server.ResourceID)
	if err != nil {
		return server, nil
	}
	review, err := data.GetInfrastructureReview(ctx, server.ReviewID)
	if err != nil {
		return server, err
	}
	if ownedResource(review, resource) != nil || !m.safeEvidence(review, resource) {
		return server, store.ErrInfrastructureChanged
	}
	if resource.PowerState == "" {
		return server, nil
	}
	if server.PowerState == resource.PowerState && server.Network == resource.Network {
		return server, nil
	}
	server.PowerState = resource.PowerState
	server.Network = resource.Network
	server.RuntimeState = "waiting"
	server.RuntimeReadyAfter = m.now()
	// Keep the revision used by mutation confirmations stable across polling.
	// This timestamp records the last persisted power or network confirmation.
	server.PowerCheckedAt = m.now()
	server.Revision++
	server.UpdatedAt = m.now()
	if err = data.UpdateManagedServer(ctx, server, server.Revision-1); err != nil {
		return server, err
	}
	return server, nil
}
