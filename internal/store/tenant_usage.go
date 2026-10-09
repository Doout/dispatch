package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// TenantUsageTotals contains only aggregate facts from one isolated tenant
// database. Builds counts executed workflow jobs, including failed jobs. Reused
// results do not consume another build or repeat the original execution time.
type TenantUsageTotals struct {
	Projects     int64
	Applications int64
	Builds       int64
	Deployments  int64
	BuildSeconds int64
	Measured     []string
}

// TenantUsage measures completed runs by finish time in [start,end). Current
// resource counts are gauges and must not be summed over multiple periods.
// Membership counts come from the catalog, not tenant-local shadow users.
func (s *SQLStore) TenantUsage(ctx context.Context, start, end time.Time) (TenantUsageTotals, error) {
	var result TenantUsageTotals
	if start.IsZero() || !end.After(start) || end.Sub(start) > 91*24*time.Hour {
		return result, errors.New("invalid tenant usage period")
	}
	opts := &sql.TxOptions{ReadOnly: true}
	if s.postgres {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&result.Projects); err != nil {
		return result, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM apps WHERE state<>'closed' AND template=FALSE`).Scan(&result.Applications); err != nil {
		return result, err
	}
	finish, lower, upper, duration := "julianday(finished_at)", "julianday(?)", "julianday(?)", "(julianday(finished_at)-julianday(started_at))*86400.0"
	if s.postgres {
		finish, lower, upper, duration = "CAST(finished_at AS TIMESTAMPTZ)", "CAST(? AS TIMESTAMPTZ)", "CAST(? AS TIMESTAMPTZ)", "EXTRACT(EPOCH FROM (CAST(finished_at AS TIMESTAMPTZ)-CAST(started_at AS TIMESTAMPTZ)))"
	}
	// The raw UTC range uses an index; the timestamp comparison keeps exact
	// boundaries despite variable fractional-second precision in stored strings.
	completed := `finished_at>=? AND finished_at<? AND state IN ('succeeded','failed','cancelled') AND finished_at IS NOT NULL AND ` + finish + `>=` + lower + ` AND ` + finish + `<` + upper
	var seconds float64
	err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN started_at IS NOT NULL AND `+duration+`>0 THEN `+duration+` ELSE 0 END),0) FROM workflow_job_results WHERE reused_from_id='' AND `+completed), stamp(start.Add(-time.Second)), stamp(end.Add(time.Second)), stamp(start), stamp(end)).Scan(&result.Builds, &seconds)
	if err != nil {
		return result, err
	}
	// SQLite's Julian-date conversion can differ by a fraction of a millisecond.
	result.BuildSeconds = int64(seconds + 0.5)
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM deployments WHERE `+completed), stamp(start.Add(-time.Second)), stamp(end.Add(time.Second)), stamp(start), stamp(end)).Scan(&result.Deployments); err != nil {
		return result, err
	}
	result.Measured = []string{"projects", "applications", "builds", "deployments", "buildSeconds"}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}
