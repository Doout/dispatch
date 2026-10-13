package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/doout/dispatch/internal/core"
)

var ErrRetentionPolicyChanged = errors.New("retention policy changed after review")
var ErrDeploymentInputsExpired = errors.New("saved deployment inputs expired under the retention policy")

const retentionBatchSize = 500

func (s *SQLStore) ApplyRetention(ctx context.Context, p core.RetentionPolicy, apply bool, now time.Time) (core.RetentionResult, error) {
	return s.applyRetention(ctx, p, apply, now, false)
}
func (s *SQLStore) ApplyRetentionReviewed(ctx context.Context, p core.RetentionPolicy, apply bool, now time.Time) (core.RetentionResult, error) {
	return s.applyRetention(ctx, p, apply, now, true)
}
func (s *SQLStore) lockRetentionProject(ctx context.Context, tx *changeTx, project string) error {
	query := `SELECT id FROM projects WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	var id string
	err := tx.QueryRowContext(ctx, s.q(query), project).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func (s *SQLStore) SetDeploymentRetentionPin(ctx context.Context, id string, pinned bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := `SELECT inputs_expired_at FROM deployments WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	var expired sql.NullString
	if err = tx.QueryRowContext(ctx, s.q(query), id).Scan(&expired); err != nil {
		return err
	}
	if pinned && expired.Valid {
		return ErrDeploymentInputsExpired
	}
	if pinned {
		if err = s.checkRuntimeReferences(ctx, tx, []string{id}); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, s.q(`UPDATE deployments SET retention_pinned=? WHERE id=?`), pinned, id)
	if err = changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

type retainedHistoryRun struct {
	id, app, state  string
	at              time.Time
	expired, pinned bool
}
type historyReferences struct {
	inputs  map[string]bool
	records map[string]bool
}

// Read references in batches instead of searching every JSON stage document
// for every deployment. Historical workflow links do not pin large inputs.
func (s *SQLStore) historyReferences(ctx context.Context, tx *changeTx, project string) (historyReferences, error) {
	refs := historyReferences{map[string]bool{}, map[string]bool{}}
	collect := func(query string, args []any, read func(*sql.Rows) error) error {
		rows, err := tx.QueryContext(ctx, s.q(query), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err = read(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	protect := func(id string) {
		if id != "" {
			refs.inputs[id], refs.records[id] = true, true
		}
	}
	err := collect(`SELECT w.deployment_ids,r.state,w.state FROM workflow_stage_runs w JOIN workflow_revisions r ON r.id=w.revision_id WHERE r.state NOT IN ('succeeded','failed','cancelled','skipped') OR w.state NOT IN ('succeeded','failed','cancelled','skipped')`, nil, func(rows *sql.Rows) error {
		var raw, revision, stage string
		if err := rows.Scan(&raw, &revision, &stage); err != nil {
			return err
		}
		var ids []string
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			return err
		}
		active := !historyTerminal(revision) || !historyTerminal(stage)
		for _, id := range ids {
			if active {
				protect(id)
			}
		}
		return nil
	})
	if err != nil {
		return refs, err
	}
	err = collect(`SELECT e.deployment_id,e.state FROM temporary_environments e WHERE e.project_id=?`, []any{project}, func(rows *sql.Rows) error {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return err
		}
		refs.records[id] = true
		if state != "closed" {
			protect(id)
		}
		return nil
	})
	if err != nil {
		return refs, err
	}
	err = collect(`SELECT p.deployment_id,r.state FROM preview_group_run_components p JOIN preview_group_runs r ON r.id=p.run_id JOIN apps a ON a.id=p.generated_app_id WHERE a.project_id=? AND p.deployment_id IS NOT NULL`, []any{project}, func(rows *sql.Rows) error {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return err
		}
		if state != "closed" {
			protect(id)
		}
		return nil
	})
	if err != nil {
		return refs, err
	}
	err = collect(`SELECT record FROM application_routes WHERE project_id=?`, []any{project}, func(rows *sql.Rows) error {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var route core.ApplicationRoute
		if err := json.Unmarshal([]byte(raw), &route); err != nil {
			return err
		}
		protect(route.DeploymentID)
		protect(route.PreviousDeploymentID)
		protect(route.RequestedDeploymentID)
		return nil
	})
	if err != nil {
		return refs, err
	}
	err = collect(`SELECT deployment_id FROM runtime_jobs WHERE project_id=? AND state NOT IN ('succeeded','failed','cancelled','unknown','acknowledged')`, []any{project}, func(rows *sql.Rows) error {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		protect(id)
		return nil
	})
	if err != nil {
		return refs, err
	}
	err = collect(`SELECT a.source_deployment_id FROM deployment_release_actions a JOIN deployments d ON d.id=a.deployment_id WHERE a.project_id=? AND d.state NOT IN ('succeeded','failed','cancelled')`, []any{project}, func(rows *sql.Rows) error {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		protect(id)
		return nil
	})
	if err != nil {
		return refs, err
	}
	// Keep the newest baseline per application, including a captured failed run.
	seen := map[string]bool{}
	err = collect(`SELECT b.deployment_id,b.app_id FROM deployment_drift_baselines b JOIN deployments d ON d.id=b.deployment_id JOIN apps a ON a.id=b.app_id WHERE a.project_id=? ORDER BY d.created_at DESC,d.id DESC`, []any{project}, func(rows *sql.Rows) error {
		var id, app string
		if err := rows.Scan(&id, &app); err != nil {
			return err
		}
		if !seen[app] {
			protect(id)
			seen[app] = true
		}
		return nil
	})
	if err != nil {
		return refs, err
	}
	err = collect(`SELECT d.id FROM deployments d JOIN apps a ON a.id=d.app_id WHERE a.project_id=? AND (EXISTS(SELECT 1 FROM deployment_runtime_artifacts r WHERE r.deployment_id=d.id) OR EXISTS(SELECT 1 FROM runtime_artifact_retirements r WHERE r.deployment_id=d.id))`, []any{project}, func(rows *sql.Rows) error {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		refs.records[id] = true
		return nil
	})
	return refs, err
}
func historyTerminal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled" || state == "skipped"
}

func (s *SQLStore) applyRetention(ctx context.Context, p core.RetentionPolicy, apply bool, now time.Time, reviewed bool) (core.RetentionResult, error) {
	out := core.RetentionResult{Applied: apply}
	if p.LogDays < 1 || p.RunDays < 1 || p.KeepRuns < 1 || p.SnapshotRetentionDays() < 1 {
		return out, errors.New("retention limits must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = s.lockRetentionProject(ctx, tx, p.ProjectID); err != nil {
		return out, err
	}
	if reviewed {
		saved := core.DefaultRetentionPolicy(p.ProjectID)
		var raw string
		err = tx.QueryRowContext(ctx, s.q(`SELECT payload FROM retention_policies WHERE project_id=?`), p.ProjectID).Scan(&raw)
		if err == nil {
			saved = core.RetentionPolicy{}
			err = json.Unmarshal([]byte(raw), &saved)
		} else if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		if err != nil {
			return out, err
		}
		if saved != p {
			return out, ErrRetentionPolicyChanged
		}
	}
	var activeReviews int
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM runtime_retention_reviews WHERE project_id=? AND lease_until>?`), p.ProjectID, stamp(now)).Scan(&activeReviews); err != nil {
		return out, err
	}
	if activeReviews > 0 {
		return out, ErrRuntimeRetentionChanged
	}
	query := `SELECT d.id,d.app_id,d.state,d.created_at,d.finished_at,d.inputs_expired_at,d.retention_pinned FROM deployments d JOIN apps a ON a.id=d.app_id WHERE a.project_id=? ORDER BY d.created_at DESC,d.id DESC`
	// Reference writers acquire these same row locks before accepting an operation.
	if s.postgres {
		query += ` FOR UPDATE OF d`
	}
	rows, err := tx.QueryContext(ctx, s.q(query), p.ProjectID)
	if err != nil {
		return out, err
	}
	runs := []retainedHistoryRun{}
	for rows.Next() {
		var r retainedHistoryRun
		var created string
		var finished, expired sql.NullString
		if err = rows.Scan(&r.id, &r.app, &r.state, &created, &finished, &expired, &r.pinned); err != nil {
			rows.Close()
			return out, err
		}
		r.at = parseTime(created)
		if finished.Valid {
			r.at = parseTime(finished.String)
		}
		r.expired = expired.Valid
		runs = append(runs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	refs, err := s.historyReferences(ctx, tx, p.ProjectID)
	if err != nil {
		return out, err
	}
	logCounts := map[string]int64{}
	rows, err = tx.QueryContext(ctx, s.q(`SELECT l.deployment_id,count(*) FROM deployment_logs l JOIN deployments d ON d.id=l.deployment_id JOIN apps a ON a.id=d.app_id WHERE a.project_id=? AND l.created_at<? GROUP BY l.deployment_id`), p.ProjectID, stamp(now.AddDate(0, 0, -p.LogDays)))
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id string
		var n int64
		if err = rows.Scan(&id, &n); err != nil {
			rows.Close()
			return out, err
		}
		logCounts[id] = n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	recent, successes := map[string]int{}, map[string]int{}
	rollbackKeep := p.KeepRollbackRevisions
	if rollbackKeep == 0 {
		rollbackKeep = p.KeepRuns
	}
	changes := 0
	for _, r := range runs {
		recent[r.app]++
		firstSuccess := false
		if r.state == "succeeded" {
			successes[r.app]++
			firstSuccess = successes[r.app] == 1
		}
		live := r.at.IsZero() || !core.DeploymentState(r.state).Terminal() || r.pinned || firstSuccess || refs.inputs[r.id]
		keep := live || recent[r.app] <= p.KeepRuns || r.state == "succeeded" && successes[r.app] <= rollbackKeep
		if keep {
			out.ProtectedRuns++
		}
		remove := !keep && !refs.records[r.id] && r.at.Before(now.AddDate(0, 0, -p.RunDays))
		expire := !keep && !r.expired && r.at.Before(now.AddDate(0, 0, -p.SnapshotRetentionDays()))
		removeLogs := !live && historyTerminal(r.state) && logCounts[r.id] > 0
		if !remove && !expire && !removeLogs {
			continue
		}
		if apply && changes >= retentionBatchSize {
			out.More = true
			continue
		}
		changes++
		if remove {
			out.Runs++
			out.Logs += logCounts[r.id]
			if apply {
				if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM deployment_logs WHERE deployment_id=?`), r.id); err != nil {
					return out, err
				}
				if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM deployments WHERE id=? AND state IN ('succeeded','failed','cancelled')`), r.id); err != nil {
					return out, err
				}
			}
			continue
		}
		if removeLogs {
			out.Logs += logCounts[r.id]
			if apply {
				if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM deployment_logs WHERE deployment_id=? AND created_at<?`), r.id, stamp(now.AddDate(0, 0, -p.LogDays))); err != nil {
					return out, err
				}
			}
		}
		if expire {
			var bytes int64
			if err = tx.QueryRowContext(ctx, s.q(`SELECT `+snapshotBytesExpression(s.postgres)+` FROM deployments WHERE id=?`), r.id).Scan(&bytes); err != nil {
				return out, err
			}
			out.Snapshots++
			if bytes > 2 {
				out.SnapshotBytes += bytes - 2
			}
			if apply {
				if _, err = tx.ExecContext(ctx, s.q(`UPDATE deployments SET spec_snapshot='{}',inputs_expired_at=? WHERE id=? AND inputs_expired_at IS NULL`), stamp(now), r.id); err != nil {
					return out, err
				}
				for _, table := range []string{"deployment_service_bindings", "deployment_drift_baselines"} {
					if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM `+table+` WHERE deployment_id=?`), r.id); err != nil {
						return out, err
					}
				}
			}
		}
	}
	// Job logs expire independently of reusable outputs and fingerprints.
	rows, err = tx.QueryContext(ctx, s.q(`SELECT j.id FROM workflow_job_results j JOIN workflow_revisions r ON r.id=j.revision_id JOIN workflow_resources f ON f.id=r.resource_id JOIN config_sources c ON c.id=f.config_source_id WHERE c.project_id=? AND j.state IN ('succeeded','failed','cancelled') AND r.state IN ('succeeded','failed','cancelled') AND COALESCE(j.finished_at,j.created_at)<? AND j.log<>'' ORDER BY j.created_at,j.id LIMIT ?`), p.ProjectID, stamp(now.AddDate(0, 0, -p.LogDays)), retentionBatchSize+1)
	if err != nil {
		return out, err
	}
	jobs := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		jobs = append(jobs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(jobs) > retentionBatchSize {
		out.More = true
		jobs = jobs[:retentionBatchSize]
	}
	out.WorkflowLogs = int64(len(jobs))
	if apply {
		for _, id := range jobs {
			if _, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_job_results SET log='' WHERE id=?`), id); err != nil {
				return out, err
			}
		}
		err = tx.Commit()
	}
	return out, err
}

func (s *SQLStore) ListAutomaticRetentionPolicies(ctx context.Context) ([]core.RetentionPolicy, error) {
	projects, err := s.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := []core.RetentionPolicy{}
	for _, project := range projects {
		p, err := s.GetRetentionPolicy(ctx, project.ID)
		if err != nil {
			return nil, fmt.Errorf("project retention: %w", err)
		}
		if !p.PauseAutomatic {
			out = append(out, p)
		}
	}
	return out, nil
}

func snapshotBytesExpression(postgres bool) string {
	if postgres {
		return "octet_length(spec_snapshot)"
	}
	return "length(CAST(spec_snapshot AS BLOB))"
}
