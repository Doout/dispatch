package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/store"
)

type Adoption struct {
	ResourceID  string `json:"resourceId"`
	Revision    int64  `json:"revision"`
	ConfirmName string `json:"confirmName"`
}

func (m *Manager) Adopt(ctx context.Context, id string, in Adoption) (core.ManagedServer, error) {
	data, err := m.lifecycle()
	if err != nil {
		return core.ManagedServer{}, err
	}
	server, err := data.GetManagedServer(ctx, id)
	if err != nil {
		return server, err
	}
	if err = m.authorize(ctx, server.ProjectID, server.ProviderID, "infrastructure.modify"); err != nil {
		return server, err
	}
	if server.Revision != in.Revision || server.Name != in.ConfirmName || !provider.ValidID(in.ResourceID) {
		return server, store.ErrInfrastructureChanged
	}
	review, err := data.GetInfrastructureReview(ctx, server.ReviewID)
	if err != nil {
		return server, err
	}
	client, _, err := m.Adapter(ctx, server.ProviderID, provider.CapabilityInspect, "")
	if err != nil {
		return server, err
	}
	resource, err := client.Server(ctx, in.ResourceID)
	if err != nil {
		return server, err
	}
	if err = ownedResource(review, resource); err != nil || resource.State != "ready" || !m.safeEvidence(review, map[string]string{"id": resource.ID, "address": resource.Address}) {
		return server, errors.New("resource does not match the reviewed ownership or is not ready")
	}
	return data.AdoptInfrastructureServer(ctx, server, resource.ID, resource.Address, m.now())
}

type DeletionReview struct {
	Server  core.ManagedServer `json:"server"`
	Digest  string             `json:"digest"`
	Summary string             `json:"summary"`
}

func deleteDigest(server core.ManagedServer) string {
	return digest(fmt.Sprintf("%s\x00%s\x00%s\x00%d", server.ID, server.ProviderID, server.ResourceID, server.Revision))
}
func (m *Manager) ReviewDelete(ctx context.Context, id string) (DeletionReview, error) {
	var result DeletionReview
	data, err := m.lifecycle()
	if err != nil {
		return result, err
	}
	server, err := data.GetManagedServer(ctx, id)
	if err != nil {
		return result, err
	}
	if err = m.authorize(ctx, server.ProjectID, server.ProviderID, "infrastructure.delete"); err != nil {
		return result, err
	}
	if server.ResourceID == "" || server.AllocationState != "allocated" {
		return result, store.ErrInfrastructureChanged
	}
	if err = data.InfrastructureDeletionBlocked(ctx, id); err != nil {
		return result, err
	}
	result = DeletionReview{Server: server, Digest: deleteDigest(server), Summary: "Permanently delete the owned provider machine and revoke its agent enrollment. All machine-local data must have been removed or explicitly preserved independently."}
	return result, nil
}
func (m *Manager) Delete(ctx context.Context, id, actor string, in Acceptance) (Accepted, error) {
	var result Accepted
	data, err := m.lifecycle()
	if err != nil {
		return result, err
	}
	server, err := data.GetManagedServer(ctx, id)
	if err != nil {
		return result, err
	}
	if err = m.authorize(ctx, server.ProjectID, server.ProviderID, "infrastructure.delete"); err != nil {
		return result, err
	}
	var oid string
	if claim, ok := core.MutationAcceptanceFromContext(ctx); ok {
		oid = claim.OperationID
	} else {
		oid, err = operationID(server.ProviderID, actor, in.RequestKey, "delete")
		if err != nil {
			return result, err
		}
	}
	if old, e := data.GetInfrastructureOperation(ctx, oid); e == nil {
		if old.ServerID != id || old.ActorID != actor || old.RequestDigest != in.Digest || in.ConfirmName != server.Name {
			return result, store.ErrInfrastructureChanged
		}
		return Accepted{Server: server, Operation: old}, nil
	}
	if in.Digest != deleteDigest(server) || in.ConfirmName != server.Name {
		return result, store.ErrInfrastructureChanged
	}
	now := m.now()
	op := core.InfrastructureOperation{ID: oid, ServerID: id, ProviderID: server.ProviderID, ActorID: actor, Action: "delete", State: "pending", Stage: "submit", ResourceID: server.ResourceID, RequestDigest: in.Digest, ExpiresAt: now.Add(30 * time.Minute), NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	if err = data.CreateInfrastructureDeletion(ctx, server, op, now, m.Admission); err != nil {
		return result, err
	}
	server, err = data.GetManagedServer(ctx, id)
	if err == nil {
		core.RecordAcceptedOperation(ctx, op.ID)
	}
	return Accepted{Server: server, Operation: op}, err
}

// DesiredConfig keeps admission input independent of secret material. Its value
// is the public reviewed selection and secret references, never resolved values.
func DesiredConfig(r core.InfrastructureReview) json.RawMessage {
	return append(json.RawMessage(nil), r.Input...)
}
