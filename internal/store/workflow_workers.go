package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

var ErrWorkerLease = errors.New("worker identity or lease changed")

type WorkflowWorkerStore interface {
	ListPrivateNetworks(context.Context) ([]core.PrivateNetwork, error)
	GetPrivateNetwork(context.Context, string) (core.PrivateNetwork, error)
	GetEdgeCredential(context.Context, string) (core.EdgeCredential, error)
	CreateWorkflowWorkerJob(context.Context, core.WorkflowWorkerJob) error
	GetWorkflowWorkerJob(context.Context, string) (core.WorkflowWorkerJob, error)
	LeaseWorkflowWorkerJob(context.Context, string, int64, string, time.Time) (*core.WorkflowWorkerJob, error)
	RenewWorkflowWorkerJob(context.Context, string, string, string, string, time.Time) (bool, error)
	CompleteWorkflowWorkerJob(context.Context, string, string, string, string, string, time.Time) error
	CancelWorkflowWorkerJob(context.Context, string, time.Time) error
}

const workerColumns = `id,node_id,generation,project_id,resource_id,revision_id,kind,mode,state,digest,request,progress,result,attempt,lease_token,lease_until,expires_at,cancel_requested,created_at,updated_at`
const workerCredentialGuard = `EXISTS(SELECT 1 FROM edge_node_credentials c WHERE c.network_id=workflow_worker_jobs.node_id AND c.generation=workflow_worker_jobs.generation AND c.revoked=FALSE AND c.public_key<>'')`

func scanWorkerJob(row scanner) (core.WorkflowWorkerJob, error) {
	var j core.WorkflowWorkerJob
	var lease, expires, created, updated string
	err := row.Scan(&j.ID, &j.NodeID, &j.Generation, &j.ProjectID, &j.ResourceID, &j.RevisionID, &j.Kind, &j.Mode, &j.State, &j.Digest, &j.Request, &j.Progress, &j.Result, &j.Attempt, &j.LeaseToken, &lease, &expires, &j.CancelRequested, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	j.LeaseUntil, j.ExpiresAt, j.CreatedAt, j.UpdatedAt = parseTime(lease), parseTime(expires), parseTime(created), parseTime(updated)
	return j, err
}
func (s *SQLStore) CreateWorkflowWorkerJob(ctx context.Context, j core.WorkflowWorkerJob) error {
	result, err := s.db.ExecContext(ctx, s.q(`INSERT INTO workflow_worker_jobs(`+workerColumns+`) SELECT ?,?,?,?,?,?,?,?,'pending',?,?,'','',0,'',?,?,FALSE,?,? WHERE EXISTS(SELECT 1 FROM edge_node_credentials WHERE network_id=? AND generation=? AND revoked=FALSE AND public_key<>'')`), j.ID, j.NodeID, j.Generation, j.ProjectID, j.ResourceID, j.RevisionID, j.Kind, j.Mode, j.Digest, j.Request, stamp(time.Time{}), stamp(j.ExpiresAt), stamp(j.CreatedAt), stamp(j.CreatedAt), j.NodeID, j.Generation)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrWorkerLease
	}
	return err
}
func (s *SQLStore) GetWorkflowWorkerJob(ctx context.Context, id string) (core.WorkflowWorkerJob, error) {
	if _, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_worker_jobs SET state=CASE WHEN attempt=0 THEN 'cancelled' ELSE 'unknown' END,request='',progress='',lease_token='',updated_at=? WHERE id=? AND state IN ('pending','running') AND (expires_at<=? OR NOT `+workerCredentialGuard+`)`), stamp(time.Now().UTC()), id, stamp(time.Now().UTC())); err != nil {
		return core.WorkflowWorkerJob{}, err
	}
	return scanWorkerJob(s.db.QueryRowContext(ctx, s.q(`SELECT `+workerColumns+` FROM workflow_worker_jobs WHERE id=?`), id))
}
func (s *SQLStore) LeaseWorkflowWorkerJob(ctx context.Context, node string, generation int64, mode string, now time.Time) (*core.WorkflowWorkerJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Serialize claims across controller processes and preserve one operation per node.
	if _, err = tx.ExecContext(ctx, s.q(`UPDATE edge_node_credentials SET updated_at=updated_at WHERE network_id=?`), node); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_worker_jobs SET state=CASE WHEN attempt=0 THEN 'cancelled' ELSE 'unknown' END,request='',lease_token='',updated_at=? WHERE node_id=? AND state IN ('pending','running') AND (expires_at<=? OR NOT `+workerCredentialGuard+`)`), stamp(now), node, stamp(now)); err != nil {
		return nil, err
	}
	j, err := scanWorkerJob(tx.QueryRowContext(ctx, s.q(`SELECT `+workerColumns+` FROM workflow_worker_jobs WHERE node_id=? AND generation=? AND mode=? AND state IN ('pending','running') AND cancel_requested=FALSE AND expires_at>? AND (state='pending' OR lease_until<=?) AND `+workerCredentialGuard+` AND NOT EXISTS(SELECT 1 FROM workflow_worker_jobs active WHERE active.node_id=? AND active.state='running' AND active.lease_until>?) ORDER BY created_at,id LIMIT 1`), node, generation, mode, stamp(now), stamp(now), node, stamp(now)))
	if errors.Is(err, ErrNotFound) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	j.State, j.Attempt, j.LeaseToken, j.LeaseUntil = "running", j.Attempt+1, ulid.Make().String(), now.Add(45*time.Second)
	if _, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_worker_jobs SET state='running',attempt=?,lease_token=?,lease_until=?,updated_at=? WHERE id=?`), j.Attempt, j.LeaseToken, stamp(j.LeaseUntil), stamp(now), j.ID); err != nil {
		return nil, err
	}
	return &j, tx.Commit()
}
func (s *SQLStore) RenewWorkflowWorkerJob(ctx context.Context, node, id, token, progress string, now time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_worker_jobs SET lease_until=?,progress=?,updated_at=? WHERE id=? AND node_id=? AND lease_token=? AND state='running' AND lease_until>? AND expires_at>? AND `+workerCredentialGuard), stamp(now.Add(45*time.Second)), progress, stamp(now), id, node, token, stamp(now), stamp(now))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, ErrWorkerLease
	}
	var cancelled bool
	err = s.db.QueryRowContext(ctx, s.q(`SELECT cancel_requested FROM workflow_worker_jobs WHERE id=?`), id).Scan(&cancelled)
	return cancelled, err
}
func (s *SQLStore) CompleteWorkflowWorkerJob(ctx context.Context, node, id, token, state, result string, now time.Time) error {
	if state != "succeeded" && state != "failed" && state != "cancelled" && state != "unknown" {
		return ErrWorkerLease
	}
	changed, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_worker_jobs SET state=?,result=?,request='',progress='',lease_token='',updated_at=? WHERE id=? AND node_id=? AND lease_token=? AND state='running' AND lease_until>? AND expires_at>? AND `+workerCredentialGuard), state, result, stamp(now), id, node, token, stamp(now), stamp(now))
	if err != nil {
		return err
	}
	n, err := changed.RowsAffected()
	if err == nil && n != 1 {
		return ErrWorkerLease
	}
	return err
}
func (s *SQLStore) CancelWorkflowWorkerJob(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_worker_jobs SET cancel_requested=TRUE,state=CASE WHEN attempt=0 THEN 'cancelled' ELSE state END,request=CASE WHEN attempt=0 THEN '' ELSE request END,updated_at=? WHERE id=? AND state IN ('pending','running')`), stamp(now), id)
	return err
}
