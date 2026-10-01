package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"time"
)

var ErrRuntimeRetentionChanged = errors.New("runtime cleanup review changed or a protected reference was acquired; review the remaining artifacts again")

// checkRuntimeReferences locks each source before checking the retirement fence.
// Reference writers and cleanup use the same row lock, including on PostgreSQL.
func (s *SQLStore) checkRuntimeReferences(ctx context.Context, tx *changeTx, ids []string) error {
	for _, id := range ids {
		if id == "" {
			continue
		}
		query := `SELECT id FROM deployments WHERE id=?`
		if s.postgres {
			query += ` FOR UPDATE`
		}
		var found string
		if err := tx.QueryRowContext(ctx, s.q(query), id).Scan(&found); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM runtime_artifact_retirements WHERE deployment_id=?`), id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrRuntimeRetentionChanged
		}
	}
	return nil
}
func (s *SQLStore) RuntimeRetentionReferences(ctx context.Context, app string, keep int) ([]core.RuntimeRetentionReference, error) {
	return s.runtimeRetentionReferences(ctx, s.db, app, keep)
}

type retentionQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *SQLStore) runtimeRetentionReferences(ctx context.Context, q retentionQuery, app string, keep int) ([]core.RuntimeRetentionReference, error) {
	rows, err := q.QueryContext(ctx, s.q(`SELECT d.id,d.created_at,d.state,
 EXISTS(SELECT 1 FROM workflow_stage_runs w WHERE w.deployment_ids LIKE '%' || d.id || '%'),
 EXISTS(SELECT 1 FROM preview_group_run_components p WHERE p.deployment_id=d.id),
 EXISTS(SELECT 1 FROM deployment_drift_baselines b WHERE b.deployment_id=d.id),
 EXISTS(SELECT 1 FROM application_routes r WHERE r.app_id=d.app_id AND r.record LIKE '%' || d.id || '%')
 FROM deployments d WHERE d.app_id=? ORDER BY d.created_at DESC,d.id DESC LIMIT 10001`), app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.RuntimeRetentionReference{}
	success := 0
	for rows.Next() {
		var r core.RuntimeRetentionReference
		var created, state string
		var workflow, preview, baseline, route bool
		if err = rows.Scan(&r.DeploymentID, &created, &state, &workflow, &preview, &baseline, &route); err != nil {
			return nil, err
		}
		r.AppID = app
		r.CreatedAt = parseTime(created)
		r.Protected = []string{}
		if state == "succeeded" {
			success++
			if success <= keep {
				r.Protected = append(r.Protected, "Retained successful rollback revision")
			}
		} else if state != "failed" && state != "cancelled" {
			r.Protected = append(r.Protected, "Deployment is active")
		}
		if workflow {
			r.Protected = append(r.Protected, "Referenced by a workflow")
		}
		if preview {
			r.Protected = append(r.Protected, "Referenced by a preview")
		}
		if baseline {
			r.Protected = append(r.Protected, "Retained drift baseline")
		}
		if route {
			r.Protected = append(r.Protected, "Published or previous route revision")
		}
		out = append(out, r)
	}
	if len(out) > 10000 {
		return nil, errors.New("deployment inventory exceeds the retention safety limit")
	}
	return out, rows.Err()
}
func (s *SQLStore) SaveRuntimeRetentionReview(ctx context.Context, r core.RuntimeRetentionReview) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO runtime_retention_reviews(id,project_id,payload) VALUES(?,?,?)`), r.ID, r.ProjectID, jsonText(r))
	return err
}
func (s *SQLStore) GetRuntimeRetentionReview(ctx context.Context, id string) (core.RuntimeRetentionReview, error) {
	var r core.RuntimeRetentionReview
	var raw string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT payload FROM runtime_retention_reviews WHERE id=?`), id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &r)
	}
	return r, err
}
func (s *SQLStore) ClaimRuntimeRetentionReview(ctx context.Context, r core.RuntimeRetentionReview, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.lockRetentionProject(ctx, tx, r.ProjectID); err != nil {
		return err
	}
	if err = s.checkRuntimeRetentionPolicy(ctx, tx, r.Policy); err != nil {
		return err
	}
	var raw, lease string
	if err = tx.QueryRowContext(ctx, s.q(`SELECT payload,lease_until FROM runtime_retention_reviews WHERE id=? AND project_id=?`), r.ID, r.ProjectID).Scan(&raw, &lease); err != nil {
		return err
	}
	var saved core.RuntimeRetentionReview
	if json.Unmarshal([]byte(raw), &saved) != nil || saved.Digest != r.Digest || lease != "" && parseTime(lease).After(now) || saved.State == "planned" && !saved.ExpiresAt.After(now) {
		return ErrRuntimeRetentionChanged
	}
	r.State = "running"
	_, err = tx.ExecContext(ctx, s.q(`UPDATE runtime_retention_reviews SET payload=?,lease_until=? WHERE id=?`), jsonText(r), stamp(now.Add(35*time.Minute)), r.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLStore) checkRuntimeRetentionPolicy(ctx context.Context, tx *changeTx, p core.RetentionPolicy) error {
	saved := core.RetentionPolicy{ProjectID: p.ProjectID, LogDays: 30, RunDays: 90, KeepRuns: 20}
	var raw string
	err := tx.QueryRowContext(ctx, s.q(`SELECT payload FROM retention_policies WHERE project_id=?`), p.ProjectID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && json.Unmarshal([]byte(raw), &saved) != nil {
		return ErrRetentionPolicyChanged
	}
	if saved != p {
		return ErrRetentionPolicyChanged
	}
	return nil
}
func (s *SQLStore) UpdateRuntimeRetentionReview(ctx context.Context, r core.RuntimeRetentionReview, finished bool) error {
	lease := ""
	if !finished {
		lease = stamp(time.Now().UTC().Add(35 * time.Minute))
	}
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE runtime_retention_reviews SET payload=?,lease_until=? WHERE id=? AND project_id=?`), jsonText(r), lease, r.ID, r.ProjectID)
	return changed(result, err)
}
func (s *SQLStore) BeginRuntimeArtifactRetirement(ctx context.Context, r core.RuntimeRetentionReview, item core.RuntimeRetentionItem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.lockRetentionProject(ctx, tx, r.ProjectID); err != nil {
		return err
	}
	if err = s.checkRuntimeRetentionPolicy(ctx, tx, r.Policy); err != nil {
		return err
	}
	query := `SELECT id FROM deployments WHERE id=? AND app_id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	var id string
	if err = tx.QueryRowContext(ctx, s.q(query), item.DeploymentID, item.AppID).Scan(&id); err != nil {
		return err
	}
	refs, err := s.runtimeRetentionReferences(ctx, tx, item.AppID, r.Policy.RollbackRetentionCount())
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if ref.DeploymentID == item.DeploymentID && len(ref.Protected) > 0 {
			return ErrRuntimeRetentionChanged
		}
	}
	var review string
	err = tx.QueryRowContext(ctx, s.q(`SELECT review_id FROM runtime_artifact_retirements WHERE deployment_id=?`), item.DeploymentID).Scan(&review)
	if err == nil {
		if review != r.ID {
			return ErrRuntimeRetentionChanged
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO runtime_artifact_retirements(deployment_id,review_id,state) VALUES(?,?,'retiring')`), item.DeploymentID, r.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLStore) FinishRuntimeArtifactRetirement(ctx context.Context, review, deployment string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE runtime_artifact_retirements SET state='retired' WHERE deployment_id=? AND review_id=?`), deployment, review)
	if err = changed(result, err); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM deployment_service_bindings WHERE deployment_id=?`), deployment); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLStore) RetiredRuntimeDeployments(ctx context.Context, app string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT r.deployment_id FROM runtime_artifact_retirements r JOIN deployments d ON d.id=r.deployment_id WHERE d.app_id=? AND r.state='retired'`), app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ValidateRuntimeRetentionMutation is repeated at remote submission, lease,
// renewal and completion. A node cannot apply an unaccepted or altered candidate.
func (s *SQLStore) ValidateRuntimeRetentionMutation(ctx context.Context, id, digest string, item core.RuntimeRetentionItem, now time.Time) error {
	r, err := s.GetRuntimeRetentionReview(ctx, id)
	if err != nil {
		return err
	}
	if r.Digest != digest || r.State != "running" {
		return ErrRuntimeRetentionChanged
	}
	p, err := s.GetRetentionPolicy(ctx, r.ProjectID)
	if err != nil {
		return err
	}
	if p != r.Policy {
		return ErrRetentionPolicyChanged
	}
	var lease string
	if err = s.db.QueryRowContext(ctx, s.q(`SELECT lease_until FROM runtime_retention_reviews WHERE id=?`), id).Scan(&lease); err != nil {
		return err
	}
	if !parseTime(lease).After(now) {
		return ErrRuntimeRetentionChanged
	}
	match := false
	for _, approved := range r.Items {
		if approved.Key == item.Key && len(approved.Protected) == 0 && jsonText(approved) == jsonText(item) {
			match = true
		}
	}
	if !match {
		return ErrRuntimeRetentionChanged
	}
	if item.Kind == "revision" {
		var review string
		if err = s.db.QueryRowContext(ctx, s.q(`SELECT review_id FROM runtime_artifact_retirements WHERE deployment_id=?`), item.DeploymentID).Scan(&review); err != nil {
			return err
		}
		if review != id {
			return ErrRuntimeRetentionChanged
		}
	}
	return nil
}
