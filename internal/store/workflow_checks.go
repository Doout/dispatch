package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

type WorkflowCheckStore interface {
	ListWorkflowChecks(context.Context, string) ([]core.WorkflowCheckReport, error)
	ClaimWorkflowCheck(context.Context, time.Time, time.Duration) (core.WorkflowCheckReport, error)
	BeginWorkflowCheckCreate(context.Context, core.WorkflowCheckReport, time.Time) error
	FinishWorkflowCheck(context.Context, core.WorkflowCheckReport, time.Time) error
}

func (s *SQLStore) createWorkflowChecks(ctx context.Context, tx eventActivityWriter, revision core.WorkflowRevision) error {
	for _, r := range revision.Checks {
		if r.ID == "" || r.RevisionID != revision.ID || r.ResourceID != revision.ResourceID || r.GitHubAppID == "" || r.ExternalID == "" || r.AppID < 1 || r.APIURL == "" {
			return errors.New("workflow check identity is incomplete")
		}
		r.State = "pending"
		r.UpdatedAt = revision.CreatedAt
		r.NextAttemptAt = revision.CreatedAt
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO workflow_check_reports(id,revision_id,resource_id,external_id,payload,app_id,api_url,next_attempt_at) VALUES(?,?,?,?,?,?,?,?)`), r.ID, r.RevisionID, r.ResourceID, r.ExternalID, jsonText(r), r.AppID, r.APIURL, stamp(r.NextAttemptAt)); err != nil {
			return err
		}
	}
	return nil
}

const workflowCheckSelect = `SELECT id,revision_id,resource_id,external_id,payload,app_id,api_url,create_state,digest,complete,next_attempt_at,lease_token,lease_until FROM workflow_check_reports`

func scanWorkflowCheck(row scanner) (core.WorkflowCheckReport, error) {
	var r core.WorkflowCheckReport
	var id, revision, resource, external, payload, next, lease string
	var appID int64
	var apiURL, create, digest, token string
	var complete bool
	err := row.Scan(&id, &revision, &resource, &external, &payload, &appID, &apiURL, &create, &digest, &complete, &next, &token, &lease)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(payload), &r); err != nil {
		return r, err
	}
	r.ID, r.RevisionID, r.ResourceID, r.ExternalID = id, revision, resource, external
	r.AppID, r.APIURL, r.CreateState, r.Digest, r.Complete, r.LeaseToken = appID, apiURL, create, digest, complete, token
	r.NextAttemptAt, r.LeaseUntil = parseTime(next), parseTime(lease)
	return r, nil
}
func (s *SQLStore) ListWorkflowChecks(ctx context.Context, revisionID string) ([]core.WorkflowCheckReport, error) {
	rows, err := s.db.QueryContext(ctx, s.q(workflowCheckSelect+` WHERE revision_id=? ORDER BY id`), revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.WorkflowCheckReport{}
	for rows.Next() {
		r, err := scanWorkflowCheck(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}
func (s *SQLStore) ClaimWorkflowCheck(ctx context.Context, now time.Time, duration time.Duration) (core.WorkflowCheckReport, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.WorkflowCheckReport{}, err
	}
	defer tx.Rollback()
	query := workflowCheckSelect + ` WHERE complete=FALSE AND next_attempt_at<=? AND lease_until<=? ORDER BY next_attempt_at,id LIMIT 1`
	if s.postgres {
		query += ` FOR UPDATE SKIP LOCKED`
	}
	r, err := scanWorkflowCheck(tx.QueryRowContext(ctx, s.q(query), stamp(now), stamp(now)))
	if err != nil {
		return r, err
	}
	r.LeaseToken, r.LeaseUntil = ulid.Make().String(), now.Add(duration)
	result, err := tx.ExecContext(ctx, s.q(`UPDATE workflow_check_reports SET lease_token=?,lease_until=? WHERE id=? AND complete=FALSE AND lease_until<=?`), r.LeaseToken, stamp(r.LeaseUntil), r.ID, stamp(now))
	if err := changed(result, err); err != nil {
		return r, err
	}
	return r, tx.Commit()
}

// Commit an external create intent before sending a POST. After an interrupted
// or ambiguous request, workers only reconcile this intent; they never recreate it.
func (s *SQLStore) BeginWorkflowCheckCreate(ctx context.Context, r core.WorkflowCheckReport, now time.Time) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_check_reports SET create_state='posting' WHERE id=? AND create_state='' AND lease_token=? AND lease_until>? AND complete=FALSE`), r.ID, r.LeaseToken, stamp(now))
	return changed(result, err)
}
func (s *SQLStore) FinishWorkflowCheck(ctx context.Context, r core.WorkflowCheckReport, now time.Time) error {
	r.UpdatedAt = now
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_check_reports SET payload=?,create_state=?,digest=?,complete=?,next_attempt_at=?,lease_token='',lease_until='' WHERE id=? AND lease_token=? AND lease_until>? AND complete=FALSE`), jsonText(r), r.CreateState, r.Digest, r.Complete, stamp(r.NextAttemptAt), r.ID, r.LeaseToken, stamp(now))
	return changed(result, err)
}
