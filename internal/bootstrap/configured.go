package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/store"
)

type configuredStore interface {
	Store
	GetManagedServer(context.Context, string) (core.ManagedServer, error)
	GetInfrastructureReview(context.Context, string) (core.InfrastructureReview, error)
}

func Configured(data configuredStore, vault *secretcrypto.Vault, publicURL string) *Manager {
	root := strings.TrimSpace(os.Getenv("DISPATCH_EDGE_BINARY_ROOT"))
	if root == "" {
		root = "/usr/local/lib/dispatch-edge"
	}
	m := &Manager{Store: data, Vault: vault, ControllerURL: publicURL, ArtifactRoot: root}
	m.ResolveTarget = func(ctx context.Context, id string) (Binding, error) {
		managed, err := data.GetManagedServer(ctx, id)
		if err == nil {
			review, e := data.GetInfrastructureReview(ctx, managed.ReviewID)
			if e != nil {
				return Binding{}, e
			}
			return Binding{ServerID: managed.ID, NodeID: managed.NodeID, ProviderID: managed.ProviderID, ProjectID: managed.ProjectID, ReviewID: managed.ReviewID, ResourceID: managed.ResourceID, Address: managed.Address, Accepted: review.State == "accepted" && managed.AllocationState == "allocated", Cancelled: managed.AllocationState == "deleted" || managed.AllocationState == "cancelled"}, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return Binding{}, err
		}
		target, err := data.GetServer(ctx, id)
		if err != nil {
			return Binding{}, err
		}
		return Binding{ServerID: target.ID, NodeID: target.AgentNodeID, ProjectID: target.ProjectID, ResourceID: target.ID, Address: target.Address, Accepted: target.Runtime == core.ServerRuntimeDocker && target.Address != "local"}, nil
	}
	return m
}

// Run resumes approved SSH work after a controller restart. Persisted leases
// prevent simultaneous installers; interrupted work reuses its reviewed plan.
func (m *Manager) Run(ctx context.Context, logger *slog.Logger) {
	workers := make(chan struct{}, 4)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		items, err := m.Store.ListTargetBootstraps(ctx, "")
		if err != nil {
			logger.Warn("Bootstrap reconciliation unavailable")
			continue
		}
		for _, item := range items {
			if ctx.Err() != nil {
				return
			}
			if item.AcceptedAt == nil || item.State == "failed" || item.State == "cancelled" {
				continue
			}
			if item.Plan.Method == "ssh" && (item.State == "accepted" || item.State == "installing" && item.LeaseUntil != nil && item.LeaseUntil.Before(m.now())) {
				select {
				case workers <- struct{}{}:
					go func(item core.TargetBootstrap) {
						defer func() { <-workers }()
						if _, e := m.InstallSSH(ctx, item.ID, item.Digest); e != nil {
							logger.Warn("Approved target installation interrupted", "bootstrapId", item.ID)
						}
					}(item)
				default:
				}
			}
			if _, err = m.Refresh(ctx, item.ID); err != nil && !errors.Is(err, ErrConflict) {
				logger.Warn("Target readiness unavailable", "bootstrapId", item.ID)
			}
		}
	}
}

// Retry explicitly resumes an interrupted SSH installation. Credentials and
// actions remain those saved in the accepted review; allocation is untouched.
func (m *Manager) Retry(ctx context.Context, id, digest string) (core.TargetBootstrap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.Store.GetTargetBootstrap(ctx, id)
	if err != nil {
		return item, err
	}
	if item.Digest != digest || item.AcceptedAt == nil || item.Plan.Method != "ssh" || item.State == "cancelled" || !item.ClaimExpiresAt.After(m.now()) {
		return item, ErrConflict
	}
	if item.State == "ready" {
		return item, nil
	}
	if item.LeaseUntil != nil && item.LeaseUntil.After(m.now()) {
		return item, ErrConflict
	}
	if _, err = m.binding(ctx, item); err != nil {
		return item, err
	}
	item.State = "accepted"
	item.LeaseUntil = nil
	item.Message = "Retry approved for the same target and pinned installation plan."
	return item, m.save(ctx, &item)
}
