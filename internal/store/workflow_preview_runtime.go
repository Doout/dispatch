package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"time"
)

const previewRuntimeGuard = ` NOT EXISTS(SELECT 1 FROM workflow_preview_apps p JOIN workflow_resources r ON r.id=p.resource_id WHERE p.app_id=runtime_jobs.app_id AND NOT (
 runtime_jobs.operation IN ('inspect','logs','retention_inspect') OR
 (runtime_jobs.operation='retention_prune' AND r.state IN ('expired','removed')) OR
 (r.active=TRUE AND r.state NOT IN ('expiring','expired','removed') AND NOT EXISTS(SELECT 1 FROM deployments d WHERE d.id=runtime_jobs.deployment_id AND d.state='cancelled')) OR
 (runtime_jobs.operation='destroy' AND EXISTS(SELECT 1 FROM workflow_preview_cleanup_apps a JOIN workflow_preview_cleanups c ON c.id=a.cleanup_id WHERE a.app_id=p.app_id AND a.job_id=runtime_jobs.id AND c.state<>'succeeded')))) `

func (s *SQLStore) checkPreviewRuntime(ctx context.Context, tx *changeTx, j *core.RuntimeJob) error {
	var resource string
	err := tx.QueryRowContext(ctx, s.q(`SELECT resource_id FROM workflow_preview_apps WHERE app_id=?`), j.AppID).Scan(&resource)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = s.lockPreviewResource(ctx, tx, resource, true); err != nil {
		return err
	}
	if j.Operation == "inspect" || j.Operation == "logs" || j.Operation == "retention_inspect" {
		return nil
	}
	var allowed int
	if j.Operation == "destroy" {
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_preview_cleanup_apps a JOIN workflow_preview_cleanups c ON c.id=a.cleanup_id WHERE a.app_id=? AND a.job_id=? AND c.state<>'succeeded'`), j.AppID, j.ID).Scan(&allowed); err != nil {
			return err
		}
		if allowed > 0 {
			return nil
		}
	}
	if j.Operation == "retention_prune" {
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_resources WHERE id=? AND state IN ('expired','removed')`), resource).Scan(&allowed); err != nil {
			return err
		}
		if allowed > 0 {
			return nil
		}
	}
	if j.DeploymentID != "" {
		var cancelled int
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM deployments WHERE id=? AND state='cancelled'`), j.DeploymentID).Scan(&cancelled); err != nil {
			return err
		}
		if cancelled > 0 {
			return ErrPreviewClosing
		}
	}
	if err = s.lockPreviewResource(ctx, tx, resource, false); err != nil {
		return err
	}
	var expiry sql.NullString
	if err = tx.QueryRowContext(ctx, s.q(`SELECT MIN(expires_at) FROM workflow_preview_triggers WHERE resource_id=? AND closed_at IS NULL`), resource).Scan(&expiry); err != nil {
		return err
	}
	if expiry.Valid {
		deadline := parseTime(expiry.String)
		if !deadline.After(time.Now().UTC()) {
			return ErrPreviewClosing
		}
		if deadline.Before(j.ExpiresAt) {
			j.ExpiresAt = deadline
		}
	}
	return nil
}
