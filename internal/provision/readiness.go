package provision

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

// ServerReadiness keeps allocation separate from authenticated runtime evidence.
// It contains no installer plans, credentials or provider connection details.
type ServerReadiness struct {
	core.ManagedServer
	WaitState       string                        `json:"waitState"`
	Deployable      bool                          `json:"deployable"`
	Bootstrap       *BootstrapReadiness           `json:"bootstrap,omitempty"`
	LatestOperation *core.InfrastructureOperation `json:"latestOperation,omitempty"`
}

type BootstrapReadiness struct {
	ID                string `json:"id"`
	State             string `json:"state"`
	InstallationState string `json:"installationState"`
	EnrollmentState   string `json:"enrollmentState"`
	RuntimeState      string `json:"runtimeState"`
}

// GetManaged checks current access before refreshing only this server. Reading
// readiness cannot accept a provider operation or start an SSH installation.
func (m *Manager) GetManaged(ctx context.Context, id string) (ServerReadiness, error) {
	var result ServerReadiness
	data, err := m.lifecycle()
	if err != nil {
		return result, err
	}
	server, err := data.GetManagedServer(ctx, id)
	if err != nil {
		return result, err
	}
	if err = m.authorize(ctx, server.ProjectID, server.ProviderID, "infrastructure.inspect"); err != nil {
		return result, err
	}
	server, err = m.refreshMachineEvidence(ctx, server)
	if err != nil {
		return result, err
	}
	if enrollment, ok := m.Store.(EnrollmentStore); ok {
		if server.AllocationState != "deleted" {
			if err = m.refreshServerReadiness(ctx, enrollment, server); err != nil {
				return result, err
			}
		}
		server, err = data.GetManagedServer(ctx, id)
		if err != nil {
			return result, err
		}
		if server.AllocationState == "allocated" {
			credential, e := enrollment.GetEdgeCredential(ctx, server.NodeID)
			if e == nil && credential.Revoked {
				server.EnrollmentState = "revoked"
			} else if e == nil && credential.PublicKey == "" && !credential.EnrollmentExpiresAt.IsZero() && !credential.EnrollmentExpiresAt.After(m.now()) {
				server.EnrollmentState = "expired"
			} else if e != nil && !errors.Is(e, store.ErrNotFound) {
				return result, e
			}
		}
	}
	result.ManagedServer = server
	if server.BootstrapID != "" {
		result.Bootstrap = &BootstrapReadiness{ID: server.BootstrapID, State: "unknown", InstallationState: "unknown", EnrollmentState: "unknown", RuntimeState: "unknown"}
		if m.Bootstrap != nil {
			b, e := m.Bootstrap.Refresh(ctx, server.BootstrapID)
			if e == nil && b.ServerID == server.ID && b.NodeID == server.NodeID && b.ProjectID == server.ProjectID && b.ProviderID == server.ProviderID {
				result.Bootstrap = &BootstrapReadiness{ID: b.ID, State: b.State, InstallationState: b.InstallationState, EnrollmentState: b.EnrollmentState, RuntimeState: b.RuntimeState}
			}
		}
	}
	ops, err := data.ListInfrastructureOperations(ctx, id)
	if err != nil {
		return result, err
	}
	for _, operation := range ops {
		if !slices.Contains([]string{"create", "restore", "delete", "server.start", "server.stop", "server.reboot", "server.promote"}, operation.Action) {
			continue
		}
		if result.LatestOperation == nil || operation.CreatedAt.After(result.LatestOperation.CreatedAt) || operation.CreatedAt.Equal(result.LatestOperation.CreatedAt) && operation.UpdatedAt.After(result.LatestOperation.UpdatedAt) {
			copy := operation
			result.LatestOperation = &copy
		}
	}
	result.WaitState = result.waitState(m.now())
	if result.WaitState == "ready" {
		// The ordinary workload target must also belong to this exact enrollment.
		if enrollment, ok := m.Store.(EnrollmentStore); ok {
			target, e := enrollment.GetServer(ctx, id)
			result.Deployable = e == nil && target.ProjectID == server.ProjectID && target.AgentNodeID == server.NodeID && target.AgentMode == "outbound-runtime" && target.State == "ready"
			if e != nil && !errors.Is(e, store.ErrNotFound) {
				return result, e
			}
		}
		if !result.Deployable {
			result.WaitState = "waiting"
		}
	}
	return result, nil
}

func (s ServerReadiness) waitState(now time.Time) string {
	switch s.AllocationState {
	case "deleted", "failed", "cancelled", "unknown":
		return s.AllocationState
	}
	if op := s.LatestOperation; op != nil {
		// A confirmed failed action or a cancellation before submission leaves
		// availability to the machine's current evidence. Its receipt still
		// records the action outcome separately from server readiness.
		settledAction := (op.State == "failed" || op.State == "cancelled") && slices.Contains([]string{"server.start", "server.stop", "server.reboot", "server.promote"}, op.Action)
		switch op.State {
		case "failed", "cancelled":
			if !settledAction {
				return op.State
			}
		case "unknown", "unresolved":
			return op.State
		case "pending_approval", "waiting_approval":
			return "pending_approval"
		case "paused":
			return "paused"
		}
		if !settledAction && op.State != "succeeded" && op.State != "adopted" {
			if !op.ExpiresAt.IsZero() && !op.ExpiresAt.After(now) {
				return "expired"
			}
			if op.CancelRequested {
				return "cancelling"
			}
			if op.Action == "delete" {
				return "deleting"
			}
			return "waiting"
		}
	}
	if s.AllocationState == "deleting" {
		return "deleting"
	}
	if b := s.Bootstrap; b != nil {
		switch b.State {
		case "failed", "cancelled", "unknown":
			return b.State
		case "planned":
			return "pending_approval"
		}
		if b.EnrollmentState == "expired" {
			return "expired"
		}
	}
	switch s.EnrollmentState {
	case "revoked", "expired":
		return s.EnrollmentState
	}
	if s.AllocationState == "allocated" && s.EnrollmentState == "enrolled" {
		if s.PowerState == "stopped" {
			return "stopped"
		}
		if s.PowerState == "transitioning" || s.PowerState == "unknown" {
			return "waiting"
		}
		if s.RuntimeState == "ready" && (s.SourceSnapshotID == "" || s.PromotionState == "promoted") {
			return "ready"
		}
		if s.RuntimeState == "verified-isolated" && s.SourceSnapshotID != "" {
			return "verified-isolated"
		}
	}
	return "waiting"
}
