package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
	"strings"
	"time"
)

var ErrPreviewHistory = errors.New("preview ownership and cleanup history must be retained; disable the configuration source instead of deleting it")

var ErrPreviewClosing = errors.New("preview is inactive or cleanup has started")

// New work and cleanup take the same resource lock. A racing worker cannot
// create an application after cleanup has captured its owned inventory.
func (s *SQLStore) lockPreviewResource(ctx context.Context, tx *changeTx, id string, allowClosed bool) error {
	if id == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE workflow_resources SET updated_at=updated_at WHERE id=?`), id); err != nil {
		return err
	}
	var temporary, active bool
	var state string
	if err := tx.QueryRowContext(ctx, s.q(`SELECT temporary,active,state FROM workflow_resources WHERE id=?`), id).Scan(&temporary, &active, &state); err != nil {
		return err
	}
	if !temporary || allowClosed {
		return nil
	}
	if !active || state == "expiring" || state == "expired" || state == "removed" {
		return ErrPreviewClosing
	}
	var expired int
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_preview_triggers WHERE resource_id=? AND closed_at IS NULL AND expires_at IS NOT NULL AND expires_at<=?`), id, stamp(time.Now().UTC())).Scan(&expired); err != nil {
		return err
	}
	if expired == 0 {
		if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM neon_replacements WHERE preview_id=? AND state IN ('accepted','unresolved')`), id).Scan(&expired); err != nil {
			return err
		}
	}
	if expired > 0 {
		return ErrPreviewClosing
	}
	return nil
}
func (s *SQLStore) checkPreviewApp(ctx context.Context, tx *changeTx, app core.App) error {
	var owned string
	err := tx.QueryRowContext(ctx, s.q(`SELECT resource_id FROM workflow_preview_apps WHERE app_id=?`), app.ID).Scan(&owned)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	id := app.HelmProvenance.WorkflowResourceID
	if owned != "" {
		if id != "" && id != owned {
			return errors.New("preview application ownership is immutable")
		}
		id = owned
	}
	if id == "" {
		return nil
	}
	if err = s.lockPreviewResource(ctx, tx, id, true); err != nil {
		return err
	}
	var current core.App
	if owned != "" {
		current, err = scanApp(tx.QueryRowContext(ctx, s.q(appSelect+` WHERE id=?`), app.ID))
		if err != nil {
			return err
		}
		if current.HelmProvenance.WorkflowResourceID != app.HelmProvenance.WorkflowResourceID || !app.Generated {
			return errors.New("preview application ownership is immutable")
		}
	}
	allowClosed := owned != "" && app.State == "closed" && current.ProjectID == app.ProjectID && current.ServerID == app.ServerID && current.SpecDigest() == app.SpecDigest()
	if err = s.lockPreviewResource(ctx, tx, id, allowClosed); err != nil {
		return err
	}
	if app.HelmProvenance.WorkflowRevisionID != "" && !allowClosed {
		var cancelled int
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_revisions WHERE id=? AND state='cancelled'`), app.HelmProvenance.WorkflowRevisionID).Scan(&cancelled); err != nil {
			return err
		}
		if cancelled > 0 {
			return ErrPreviewClosing
		}
	}
	var project string
	var temporary bool
	if err = tx.QueryRowContext(ctx, s.q(`SELECT c.project_id,r.temporary FROM workflow_resources r JOIN config_sources c ON c.id=r.config_source_id WHERE r.id=?`), id).Scan(&project, &temporary); err != nil {
		return err
	}
	if temporary && (!app.Generated || app.ProjectID != project) {
		return errors.New("preview app must be generated in its owning project")
	}
	return nil
}
func (s *SQLStore) recordPreviewApp(ctx context.Context, tx *changeTx, app core.App) error {
	if app.HelmProvenance.WorkflowResourceID == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, s.q(`INSERT INTO workflow_preview_apps(app_id,resource_id) SELECT ?,id FROM workflow_resources WHERE id=? AND temporary=TRUE ON CONFLICT(app_id) DO NOTHING`), app.ID, app.HelmProvenance.WorkflowResourceID)
	return err
}
func (s *SQLStore) checkPreviewDeployment(ctx context.Context, tx *changeTx, appID string) error {
	var id string
	err := tx.QueryRowContext(ctx, s.q(`SELECT resource_id FROM workflow_preview_apps WHERE app_id=?`), appID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = s.lockPreviewResource(ctx, tx, id, false); err != nil {
		return err
	}
	app, err := scanApp(tx.QueryRowContext(ctx, s.q(appSelect+` WHERE id=?`), appID))
	if err != nil {
		return err
	}
	if app.HelmProvenance.WorkflowRevisionID != "" {
		var cancelled int
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_revisions WHERE id=? AND state='cancelled'`), app.HelmProvenance.WorkflowRevisionID).Scan(&cancelled); err != nil {
			return err
		}
		if cancelled > 0 {
			return ErrPreviewClosing
		}
	}
	return nil
}

// Legacy stage evidence may claim a generated application in the same project.
// Conflicting provenance and ownership always stop cleanup.
func (s *SQLStore) RegisterLegacyPreviewApp(ctx context.Context, resourceID, appID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.lockPreviewResource(ctx, tx, resourceID, true); err != nil {
		return err
	}
	app, err := scanApp(tx.QueryRowContext(ctx, s.q(appSelect+` WHERE id=?`), appID))
	if err != nil {
		return err
	}
	var project string
	if err = tx.QueryRowContext(ctx, s.q(`SELECT c.project_id FROM workflow_resources r JOIN config_sources c ON c.id=r.config_source_id WHERE r.id=? AND r.temporary=TRUE`), resourceID).Scan(&project); err != nil {
		return err
	}
	if !app.Generated || app.ProjectID != project || app.HelmProvenance.WorkflowResourceID != "" && app.HelmProvenance.WorkflowResourceID != resourceID {
		return errors.New("preview history points to an application it does not own")
	}
	if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO workflow_preview_apps(app_id,resource_id) VALUES(?,?) ON CONFLICT(app_id) DO NOTHING`), appID, resourceID); err != nil {
		return err
	}
	var owner string
	if err = tx.QueryRowContext(ctx, s.q(`SELECT resource_id FROM workflow_preview_apps WHERE app_id=?`), appID).Scan(&owner); err != nil {
		return err
	}
	if owner != resourceID {
		return errors.New("application belongs to another preview")
	}
	return tx.Commit()
}

const previewCleanupColumns = `id,resource_id,reason,final_state,state,error,attempts,created_at,updated_at,lease_token,lease_until`

func scanPreviewCleanup(row scanner) (core.WorkflowPreviewCleanup, error) {
	var c core.WorkflowPreviewCleanup
	var created, updated, lease string
	err := row.Scan(&c.ID, &c.ResourceID, &c.Reason, &c.FinalState, &c.State, &c.Error, &c.Attempts, &created, &updated, &c.LeaseToken, &lease)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	c.CreatedAt, c.UpdatedAt, c.LeaseUntil = parseTime(created), parseTime(updated), parseTime(lease)
	return c, err
}
func (s *SQLStore) ListWorkflowPreviewCleanups(ctx context.Context, resourceID string) ([]core.WorkflowPreviewCleanup, error) {
	query := `SELECT ` + previewCleanupColumns + ` FROM workflow_preview_cleanups`
	args := []any{}
	if resourceID != "" {
		query += ` WHERE resource_id=?`
		args = append(args, resourceID)
	} else {
		query += ` WHERE state<>'succeeded'`
	}
	query += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	result := []core.WorkflowPreviewCleanup{}
	for rows.Next() {
		c, e := scanPreviewCleanup(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		result = append(result, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range result {
		result[i].Services, err = s.neonCleanupServices(ctx, result[i].ID)
		if err != nil {
			return nil, err
		}
		result[i].Apps, err = s.previewCleanupApps(ctx, result[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (s *SQLStore) previewCleanupApps(ctx context.Context, id string) ([]core.WorkflowPreviewCleanupApp, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT app_id,server_id,spec_digest,job_id,state,error,node_id,node_generation,previous_job_ids FROM workflow_preview_cleanup_apps WHERE cleanup_id=? ORDER BY app_id`), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := []core.WorkflowPreviewCleanupApp{}
	for rows.Next() {
		var a core.WorkflowPreviewCleanupApp
		var previous string
		if err = rows.Scan(&a.AppID, &a.ServerID, &a.SpecDigest, &a.JobID, &a.State, &a.Error, &a.NodeID, &a.NodeGeneration, &previous); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(previous), &a.PreviousJobIDs); err != nil {
			return nil, err
		}
		apps = append(apps, a)
	}
	return apps, rows.Err()
}

// Intent, owned inventory and mutation fences commit before any external call.
// Expiry checks the current deadline, allowing an earlier extension to win.
func (s *SQLStore) BeginWorkflowPreviewCleanup(ctx context.Context, resourceID, reason string, now time.Time) (core.WorkflowPreviewCleanup, error) {
	var c core.WorkflowPreviewCleanup
	if reason != "expired" && reason != "removed" && reason != "closed" {
		return c, errors.New("invalid preview cleanup reason")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	if err = s.lockPreviewResource(ctx, tx, resourceID, true); err != nil {
		return c, err
	}
	c, err = scanPreviewCleanup(tx.QueryRowContext(ctx, s.q(`SELECT `+previewCleanupColumns+` FROM workflow_preview_cleanups WHERE resource_id=? AND state<>'succeeded'`), resourceID))
	if err == nil {
		if reason != "expired" && c.FinalState != "removed" {
			c.FinalState = "removed"
			_, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_preview_cleanups SET final_state='removed' WHERE id=?`), c.ID)
		}
		if err != nil {
			return c, err
		}
		return c, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return c, err
	}
	var temporary bool
	var state string
	if err = tx.QueryRowContext(ctx, s.q(`SELECT temporary,state FROM workflow_resources WHERE id=?`), resourceID).Scan(&temporary, &state); err != nil {
		return c, err
	}
	if !temporary || state == "removed" || reason == "expired" && state == "expired" {
		return c, ErrPreviewClosing
	}
	if reason == "expired" {
		var count int
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_preview_triggers WHERE resource_id=? AND closed_at IS NULL AND expires_at IS NOT NULL AND expires_at<=?`), resourceID, stamp(now)).Scan(&count); err != nil {
			return c, err
		}
		if count == 0 {
			return c, ErrPreviewClosing
		}
	}
	c = core.WorkflowPreviewCleanup{ID: ulid.Make().String(), ResourceID: resourceID, Reason: reason, FinalState: "removed", State: "pending", CreatedAt: now, UpdatedAt: now}
	if reason == "expired" {
		c.FinalState = "expired"
	}
	if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO workflow_preview_cleanups(`+previewCleanupColumns+`) VALUES(?,?,?,?,?,'',0,?,?,'','')`), c.ID, c.ResourceID, c.Reason, c.FinalState, c.State, stamp(now), stamp(now)); err != nil {
		return c, err
	}
	if _, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_resources SET active=FALSE,state='expiring',last_error='',updated_at=? WHERE id=?`), stamp(now), resourceID); err != nil {
		return c, err
	}
	rows, err := tx.QueryContext(ctx, s.q(appSelect+` WHERE id IN(SELECT app_id FROM workflow_preview_apps WHERE resource_id=?)`), resourceID)
	if err != nil {
		return c, err
	}
	apps := []core.App{}
	for rows.Next() {
		a, e := scanApp(rows)
		if e != nil {
			rows.Close()
			return c, e
		}
		apps = append(apps, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return c, err
	}
	for _, a := range apps {
		state := "pending"
		if a.State == "closed" {
			state = "succeeded"
		}
		var nodeID string
		var generation int64
		if err = tx.QueryRowContext(ctx, s.q(`SELECT agent_node_id FROM servers WHERE id=?`), a.ServerID).Scan(&nodeID); err != nil {
			return c, err
		}
		if nodeID != "" {
			if err = tx.QueryRowContext(ctx, s.q(`SELECT generation FROM edge_node_credentials WHERE network_id=?`), nodeID).Scan(&generation); err != nil {
				return c, err
			}
		}
		if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO workflow_preview_cleanup_apps(cleanup_id,app_id,server_id,spec_digest,job_id,state,node_id,node_generation) VALUES(?,?,?,?,?,?,?,?)`), c.ID, a.ID, a.ServerID, a.SpecDigest(), "preview-cleanup-"+c.ID+"-"+a.ID, state, nodeID, generation); err != nil {
			return c, err
		}
	}
	if err = s.captureNeonCleanupServices(ctx, tx, c); err != nil {
		return c, err
	}
	if err = s.fencePreviewMutations(ctx, tx, resourceID, "Preview cleanup requested", now); err != nil {
		return c, err
	}
	return c, tx.Commit()
}
func (s *SQLStore) fencePreviewMutations(ctx context.Context, tx *changeTx, resourceID, reason string, now time.Time) error {
	// Dispatched operations can have changed the target. Preserve uncertainty
	// and their old lease deadlines for inspection and acknowledgment.
	_, err := tx.ExecContext(ctx, s.q(`UPDATE runtime_jobs SET state=CASE WHEN state='pending' AND attempt=0 THEN 'cancelled' ELSE 'unknown' END,cancel_requested=TRUE,encrypted_request='',lease_token='',message=?,updated_at=? WHERE app_id IN(SELECT app_id FROM workflow_preview_apps WHERE resource_id=?) AND state IN ('pending','running') AND operation NOT IN ('inspect','logs','storage_inspect','service_inspect','retention_inspect')`), reason, stamp(now), resourceID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.q(`UPDATE deployments SET state='cancelled',message=?,finished_at=? WHERE app_id IN(SELECT app_id FROM workflow_preview_apps WHERE resource_id=?) AND state NOT IN ('succeeded','failed','cancelled')`), reason, stamp(now), resourceID)
	return err
}
func (s *SQLStore) ClaimWorkflowPreviewCleanup(ctx context.Context, id string, now time.Time) (*core.WorkflowPreviewCleanup, error) {
	token := ulid.Make().String()
	until := now.Add(6 * time.Minute)
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_cleanups SET lease_token=?,lease_until=?,attempts=attempts+1,updated_at=? WHERE id=? AND state<>'succeeded' AND lease_until<=?`), token, stamp(until), stamp(now), id, stamp(now))
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return nil, err
	}
	c, err := scanPreviewCleanup(s.db.QueryRowContext(ctx, s.q(`SELECT `+previewCleanupColumns+` FROM workflow_preview_cleanups WHERE id=? AND lease_token=?`), id, token))
	if err != nil {
		return nil, err
	}
	c.Apps, err = s.previewCleanupApps(ctx, id)
	if err == nil {
		c.Services, err = s.neonCleanupServices(ctx, id)
	}
	return &c, err
}
func (s *SQLStore) SaveWorkflowPreviewCleanupApp(ctx context.Context, c core.WorkflowPreviewCleanup, a core.WorkflowPreviewCleanupApp) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_cleanup_apps SET state=?,error=? WHERE cleanup_id=? AND app_id=? AND job_id=? AND EXISTS(SELECT 1 FROM workflow_preview_cleanups WHERE id=? AND lease_token=? AND lease_until>?)`), a.State, a.Error, c.ID, a.AppID, a.JobID, c.ID, c.LeaseToken, stamp(time.Now().UTC()))
	return changed(result, err)
}
func (s *SQLStore) FinishWorkflowPreviewCleanup(ctx context.Context, c core.WorkflowPreviewCleanup, detail string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.lockPreviewResource(ctx, tx, c.ResourceID, true); err != nil {
		return err
	}
	state, resourceState := "blocked", "expiring"
	if detail == "" {
		var incomplete int
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_preview_cleanup_apps WHERE cleanup_id=? AND state<>'succeeded'`), c.ID).Scan(&incomplete); err != nil {
			return err
		}
		if incomplete > 0 {
			return fmt.Errorf("preview cleanup has %d incomplete applications", incomplete)
		}
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_preview_cleanup_apps p WHERE p.cleanup_id=? AND (NOT EXISTS(SELECT 1 FROM apps WHERE id=p.app_id AND state='closed') OR EXISTS(SELECT 1 FROM runtime_jobs j WHERE j.app_id=p.app_id AND j.state IN ('pending','running','unknown') AND j.operation NOT IN ('inspect','logs','storage_inspect','service_inspect','retention_inspect')))`), c.ID).Scan(&incomplete); err != nil {
			return err
		}
		if incomplete > 0 {
			return errors.New("preview cleanup still has unresolved runtime ownership")
		}
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_preview_cleanup_services WHERE cleanup_id=? AND state<>'succeeded'`), c.ID).Scan(&incomplete); err != nil {
			return err
		}
		if incomplete > 0 {
			return errors.New("preview cleanup still has unresolved owned databases")
		}
		// Explicit removal can supersede an expiry while cleanup is in progress.
		if err = tx.QueryRowContext(ctx, s.q(`SELECT final_state FROM workflow_preview_cleanups WHERE id=?`), c.ID).Scan(&resourceState); err != nil {
			return err
		}
		state = "succeeded"
	}
	result, err := tx.ExecContext(ctx, s.q(`UPDATE workflow_preview_cleanups SET state=?,error=?,updated_at=?,lease_token='',lease_until='' WHERE id=? AND lease_token=? AND lease_until>?`), state, detail, stamp(now), c.ID, c.LeaseToken, stamp(now))
	if err = changed(result, err); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_resources SET active=FALSE,state=?,last_error=?,updated_at=? WHERE id=?`), resourceState, detail, stamp(now), c.ResourceID); err != nil {
		return err
	}
	if resourceState == "removed" {
		_, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET closed_at=COALESCE(closed_at,?),lifetime_report_pending=TRUE WHERE resource_id=?`), stamp(now), c.ResourceID)
	} else {
		_, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET lifetime_report_pending=TRUE WHERE resource_id=?`), c.ResourceID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Certain failures and acknowledged unknown outcomes can start a new attempt.
// Pending/running/unknown jobs always retain their original identity.
func (s *SQLStore) RetryWorkflowPreviewCleanupApp(ctx context.Context, c core.WorkflowPreviewCleanup, a core.WorkflowPreviewCleanupApp) (core.WorkflowPreviewCleanupApp, error) {
	next := "preview-cleanup-" + ulid.Make().String()
	previous := append(append([]string{}, a.PreviousJobIDs...), a.JobID)
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_cleanup_apps SET job_id=?,state='pending',error='',previous_job_ids=? WHERE cleanup_id=? AND app_id=? AND job_id=? AND EXISTS(SELECT 1 FROM runtime_jobs WHERE id=? AND state IN ('failed','cancelled','acknowledged')) AND EXISTS(SELECT 1 FROM workflow_preview_cleanups WHERE id=? AND lease_token=? AND lease_until>?)`), next, jsonText(previous), c.ID, a.AppID, a.JobID, a.JobID, c.ID, c.LeaseToken, stamp(time.Now().UTC()))
	if err != nil {
		return a, err
	}
	n, err := result.RowsAffected()
	if n > 0 {
		a.JobID, a.State, a.Error = next, "pending", ""
		a.PreviousJobIDs = previous
	}
	return a, err
}
func (s *SQLStore) retiredPreviewCommand(ctx context.Context, tx *changeTx, trigger core.WorkflowPreviewTrigger) (bool, error) {
	incoming := trigger.LifetimeStartCommentID
	if incoming == "" || strings.Trim(incoming, "0123456789") != "" {
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, s.q(`SELECT t.lifetime_start_comment_id,COALESCE(c.comment_id,'') FROM workflow_preview_triggers t LEFT JOIN workflow_preview_comments c ON c.trigger_id=t.id WHERE t.github_app_id=? AND lower(t.repository)=lower(?) AND t.pull_request_number=? AND t.command=? AND t.closed_at IS NOT NULL`), trigger.GitHubAppID, trigger.Repository, trigger.PullRequestNumber, trigger.Command)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var start, comment string
		if err = rows.Scan(&start, &comment); err != nil {
			return false, err
		}
		for _, id := range []string{start, comment} {
			if id != "" && strings.Trim(id, "0123456789") == "" && (len(id) > len(incoming) || len(id) == len(incoming) && id >= incoming) {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

func (s *SQLStore) CheckWorkflowPreviewCleanup(ctx context.Context, appID, operation string) error {
	var owned int
	if err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_preview_apps WHERE app_id=?`), appID).Scan(&owned); err != nil {
		return err
	}
	if owned == 0 {
		return nil
	}
	var accepted int
	if err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_preview_cleanup_apps a JOIN workflow_preview_cleanups c ON c.id=a.cleanup_id WHERE a.app_id=? AND a.job_id=? AND c.state<>'succeeded' AND c.lease_until>?`), appID, operation, stamp(time.Now().UTC())).Scan(&accepted); err != nil {
		return err
	}
	if accepted == 0 {
		return ErrPreviewClosing
	}
	return nil
}
