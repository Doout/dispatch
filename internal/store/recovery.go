package store

import (
	"context"
	"github.com/doout/dispatch/internal/core"
	"time"
)

func (s *SQLStore) RenewDeploymentLease(ctx context.Context, id string, until time.Time) error {
	return changed(s.db.ExecContext(ctx, s.q(`UPDATE deployments SET lease_until=? WHERE id=? AND state NOT IN ('succeeded','failed','cancelled')`), stamp(until), id))
}

// Reclaim only expired work. Runtime writes cannot safely be replayed after a crash.
func (s *SQLStore) RecoverInterruptedDeployments(ctx context.Context, now, startup time.Time) ([]core.Deployment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, s.q(`SELECT id,app_id FROM deployments WHERE state NOT IN ('succeeded','failed','cancelled') AND ((lease_until IS NOT NULL AND lease_until<?) OR (lease_until IS NULL AND created_at<?))`), stamp(now), stamp(startup))
	if err != nil {
		return nil, err
	}
	out := []core.Deployment{}
	for rows.Next() {
		var d core.Deployment
		if err = rows.Scan(&d.ID, &d.AppID); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, d)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	recovered := []core.Deployment{}
	for _, d := range out {
		d.State = core.DeploymentFailed
		d.FinishedAt = &now
		d.Message = "Controller lost contact with this execution. Inspect the running release before retrying; no runtime changes were replayed."
		result, err := tx.ExecContext(ctx, s.q(`UPDATE deployments SET state='failed',message=?,finished_at=?,lease_until=NULL WHERE id=? AND state NOT IN ('succeeded','failed','cancelled') AND ((lease_until IS NOT NULL AND lease_until<?) OR (lease_until IS NULL AND created_at<?))`), d.Message, stamp(now), d.ID, stamp(now), stamp(startup))
		if err != nil {
			return nil, err
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			continue
		}
		if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO deployment_logs(deployment_id,level,message,created_at) VALUES(?,?,?,?)`), d.ID, "error", d.Message, stamp(now)); err != nil {
			return nil, err
		}
		recovered = append(recovered, d)
	}
	return recovered, tx.Commit()
}
