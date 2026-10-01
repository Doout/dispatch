package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/doout/dispatch/internal/core"
)

// Owned snapshot rows are the quota ledger. Pending and unknown rows count until
// cancellation before submission or an authoritative provider deletion commits.
func (s *SQLStore) applySnapshotQuota(ctx context.Context, tx *sql.Tx, in core.InfrastructureAcceptance) error {
	if tx == nil || in.SnapshotID == "" || in.ProjectID == "" || in.ProviderID == "" || in.OperationID == "" {
		return ErrQuotaReservationChanged
	}
	if in.Action != "snapshot.create" {
		switch in.Action {
		case "snapshot.created", "snapshot.deleted", "snapshot.cancelled", "snapshot.unresolved":
			return nil
		}
		return ErrQuotaReservationChanged
	}
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE project_infrastructure_policies SET revision=revision WHERE project_id=?`), in.ProjectID); err != nil {
		return err
	}
	p, err := scanQuotaPolicy(tx.QueryRowContext(ctx, s.q(`SELECT `+quotaPolicyColumns+` FROM project_infrastructure_policies WHERE project_id=?`), in.ProjectID))
	if errors.Is(err, ErrNotFound) {
		return &core.InfrastructureQuotaViolation{Code: "policy_required", Limit: "maxSnapshots", Maximum: 0, Requested: 1}
	}
	if err != nil {
		return err
	}
	allowed := false
	for _, rule := range p.Providers {
		if rule.ProviderID == in.ProviderID {
			allowed = true
			break
		}
	}
	if !allowed {
		return &core.InfrastructureQuotaViolation{Code: "allocation_disallowed", Limit: "provider", ProviderID: in.ProviderID, Requested: 1}
	}
	var used int64
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM infrastructure_snapshots WHERE project_id=? AND state NOT IN ('deleted','cancelled')`), in.ProjectID).Scan(&used); err != nil {
		return err
	}
	if p.MaxSnapshots >= 0 && used+1 > p.MaxSnapshots {
		return &core.InfrastructureQuotaViolation{Code: "quota_exceeded", Limit: "maxSnapshots", Maximum: p.MaxSnapshots, Usage: used, Requested: 1}
	}
	return nil
}
