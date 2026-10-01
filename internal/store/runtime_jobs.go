package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

var ErrRuntimeJobConflict = errors.New("runtime operation identity or lease changed")

type RuntimeJobStore interface {
	GetEdgeCredential(context.Context, string) (core.EdgeCredential, error)
	GetServiceProvisionRun(context.Context, string) (core.ServiceProvisionRun, error)
	CreateRuntimeJob(context.Context, core.RuntimeJob) error
	GetRuntimeJob(context.Context, string) (core.RuntimeJob, error)
	LeaseRuntimeJob(context.Context, string, time.Time, time.Duration) (*core.RuntimeJob, error)
	RenewRuntimeJob(context.Context, string, string, string, string, string, time.Time, time.Duration) (bool, error)
	CompleteRuntimeJob(context.Context, string, string, string, string, string, time.Time) error
	CancelRuntimeJob(context.Context, string, time.Time) error
	ExpireRuntimeJobs(context.Context, time.Time) error
	ActiveRuntimeMutation(context.Context, string) (*core.RuntimeJob, error)
	AcknowledgeRuntimeJob(context.Context, string, string, string, time.Time) error
}

const runtimeJobColumns = `id,server_id,node_id,node_generation,attempt,project_id,app_id,deployment_id,service_run_id,operation,state,request_digest,encrypted_request,encrypted_result,lease_token,lease_until,cancel_requested,phase,message,expires_at,created_at,updated_at`

func (s *SQLStore) CreateRuntimeJob(ctx context.Context, j core.RuntimeJob) error {
	values := []any{j.ID, j.ServerID, j.NodeID, j.NodeGeneration, 0, j.ProjectID, j.AppID, j.DeploymentID, j.ServiceRunID, j.Operation, "pending", j.RequestDigest, j.EncryptedRequest, "", "", stamp(time.Time{}), false, "", "", stamp(j.ExpiresAt), stamp(j.CreatedAt), stamp(j.CreatedAt)}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
	values = append(values, j.ServerID, j.NodeID, j.AppID, j.ProjectID, j.ServerID, j.Operation, j.ServiceRunID, j.ProjectID, j.NodeID, j.NodeGeneration)
	result, err := s.db.ExecContext(ctx, s.q(`INSERT INTO runtime_jobs(`+runtimeJobColumns+`) SELECT `+placeholders+` WHERE EXISTS (SELECT 1 FROM servers WHERE id=? AND agent_node_id=?) AND (EXISTS (SELECT 1 FROM apps WHERE id=? AND project_id=? AND server_id=?) OR (?='provision_service' AND EXISTS (SELECT 1 FROM service_provision_runs WHERE id=? AND project_id=?))) AND EXISTS (SELECT 1 FROM edge_node_credentials WHERE network_id=? AND generation=? AND revoked=FALSE AND public_key<>'') ON CONFLICT(id) DO NOTHING`), values...)
	if err != nil {
		if active, checkErr := s.ActiveRuntimeMutation(ctx, j.AppID); checkErr == nil && active != nil {
			return ErrRuntimeJobConflict
		}
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	existing, err := s.GetRuntimeJob(ctx, j.ID)
	if err != nil {
		return ErrRuntimeJobConflict
	}
	if existing.NodeGeneration != j.NodeGeneration || existing.RequestDigest != j.RequestDigest || existing.ServerID != j.ServerID || existing.NodeID != j.NodeID || existing.ProjectID != j.ProjectID || existing.AppID != j.AppID || existing.Operation != j.Operation {
		return ErrRuntimeJobConflict
	}
	return nil
}

func scanRuntimeJob(row scanner) (core.RuntimeJob, error) {
	var j core.RuntimeJob
	var lease, expires, created, updated string
	err := row.Scan(&j.ID, &j.ServerID, &j.NodeID, &j.NodeGeneration, &j.Attempt, &j.ProjectID, &j.AppID, &j.DeploymentID, &j.ServiceRunID, &j.Operation, &j.State, &j.RequestDigest, &j.EncryptedRequest, &j.EncryptedResult, &j.LeaseToken, &lease, &j.CancelRequested, &j.Phase, &j.Message, &expires, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	j.LeaseUntil, j.ExpiresAt, j.CreatedAt, j.UpdatedAt = parseTime(lease), parseTime(expires), parseTime(created), parseTime(updated)
	return j, err
}

func (s *SQLStore) GetRuntimeJob(ctx context.Context, id string) (core.RuntimeJob, error) {
	return scanRuntimeJob(s.db.QueryRowContext(ctx, s.q(`SELECT `+runtimeJobColumns+` FROM runtime_jobs WHERE id=?`), id))
}

func (s *SQLStore) LeaseRuntimeJob(ctx context.Context, node string, now time.Time, duration time.Duration) (*core.RuntimeJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if s.postgres {
		var id string
		if err = tx.QueryRowContext(ctx, s.q(`SELECT network_id FROM edge_node_credentials WHERE network_id=? FOR UPDATE`), node).Scan(&id); errors.Is(err, sql.ErrNoRows) {
			return nil, tx.Commit()
		} else if err != nil {
			return nil, err
		}
	}
	_, err = tx.ExecContext(ctx, s.q(`UPDATE runtime_jobs SET state='unknown',encrypted_request='',lease_token='',message='The runtime deadline expired; inspect the target before retrying.',updated_at=? WHERE node_id=? AND state IN ('pending','running') AND expires_at<=?`), stamp(now), node, stamp(now))
	if err != nil {
		return nil, err
	}
	query := `SELECT ` + runtimeJobColumns + ` FROM runtime_jobs WHERE node_id=? AND NOT EXISTS (SELECT 1 FROM runtime_jobs active WHERE active.node_id=runtime_jobs.node_id AND active.state='running' AND active.lease_until>?) AND state IN ('pending','running') AND (state='pending' OR lease_until<=?) AND expires_at>? AND EXISTS (SELECT 1 FROM servers WHERE servers.id=runtime_jobs.server_id AND servers.agent_node_id=runtime_jobs.node_id) AND (EXISTS (SELECT 1 FROM apps WHERE apps.id=runtime_jobs.app_id AND apps.project_id=runtime_jobs.project_id AND apps.server_id=runtime_jobs.server_id) OR (runtime_jobs.operation='provision_service' AND EXISTS (SELECT 1 FROM service_provision_runs WHERE id=runtime_jobs.service_run_id AND project_id=runtime_jobs.project_id))) AND EXISTS (SELECT 1 FROM edge_node_credentials WHERE network_id=runtime_jobs.node_id AND generation=runtime_jobs.node_generation AND revoked=FALSE AND public_key<>'') ORDER BY created_at LIMIT 1`
	if s.postgres {
		query += ` FOR UPDATE SKIP LOCKED`
	}
	j, err := scanRuntimeJob(tx.QueryRowContext(ctx, s.q(query), node, stamp(now), stamp(now), stamp(now)))
	if errors.Is(err, ErrNotFound) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	j.Attempt++
	j.State, j.LeaseToken, j.LeaseUntil, j.UpdatedAt = "running", ulid.Make().String(), now.Add(duration), now
	result, err := tx.ExecContext(ctx, s.q(`UPDATE runtime_jobs SET state='running',attempt=attempt+1,lease_token=?,lease_until=?,updated_at=? WHERE id=? AND state IN ('pending','running') AND (state='pending' OR lease_until<=?)`), j.LeaseToken, stamp(j.LeaseUntil), stamp(now), j.ID, stamp(now))
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrRuntimeJobConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *SQLStore) RenewRuntimeJob(ctx context.Context, node, id, token, phase, message string, now time.Time, duration time.Duration) (bool, error) {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE runtime_jobs SET lease_until=?,phase=?,message=?,updated_at=? WHERE id=? AND node_id=? AND lease_token=? AND state='running' AND lease_until>? AND expires_at>? AND EXISTS (SELECT 1 FROM servers WHERE servers.id=runtime_jobs.server_id AND servers.agent_node_id=runtime_jobs.node_id) AND (EXISTS (SELECT 1 FROM apps WHERE apps.id=runtime_jobs.app_id AND apps.project_id=runtime_jobs.project_id AND apps.server_id=runtime_jobs.server_id) OR (runtime_jobs.operation='provision_service' AND EXISTS (SELECT 1 FROM service_provision_runs WHERE id=runtime_jobs.service_run_id AND project_id=runtime_jobs.project_id))) AND EXISTS (SELECT 1 FROM edge_node_credentials WHERE network_id=runtime_jobs.node_id AND generation=runtime_jobs.node_generation AND revoked=FALSE AND public_key<>'')`), stamp(now.Add(duration)), phase, message, stamp(now), id, node, token, stamp(now), stamp(now))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, ErrRuntimeJobConflict
	}
	j, err := s.GetRuntimeJob(ctx, id)
	return j.CancelRequested, err
}

func (s *SQLStore) CompleteRuntimeJob(ctx context.Context, node, id, token, state, encrypted string, now time.Time) error {
	if state != "succeeded" && state != "failed" && state != "cancelled" && state != "unknown" {
		return ErrRuntimeJobConflict
	}
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE runtime_jobs SET state=?,encrypted_result=?,encrypted_request='',lease_token='',updated_at=? WHERE id=? AND node_id=? AND lease_token=? AND state='running' AND lease_until>? AND expires_at>? AND EXISTS (SELECT 1 FROM servers WHERE servers.id=runtime_jobs.server_id AND servers.agent_node_id=runtime_jobs.node_id) AND (EXISTS (SELECT 1 FROM apps WHERE apps.id=runtime_jobs.app_id AND apps.project_id=runtime_jobs.project_id AND apps.server_id=runtime_jobs.server_id) OR (runtime_jobs.operation='provision_service' AND EXISTS (SELECT 1 FROM service_provision_runs WHERE id=runtime_jobs.service_run_id AND project_id=runtime_jobs.project_id))) AND EXISTS (SELECT 1 FROM edge_node_credentials WHERE network_id=runtime_jobs.node_id AND generation=runtime_jobs.node_generation AND revoked=FALSE AND public_key<>'')`), state, encrypted, stamp(now), id, node, token, stamp(now), stamp(now))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrRuntimeJobConflict
	}
	return nil
}

func (s *SQLStore) CancelRuntimeJob(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE runtime_jobs SET cancel_requested=TRUE,updated_at=? WHERE id=? AND state IN ('pending','running')`), stamp(now), id)
	return err
}

// ExpireRuntimeJobs removes credentials even when a disconnected agent never
// polls again. An expired lease alone does not establish a mutation's outcome.
func (s *SQLStore) ExpireRuntimeJobs(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE runtime_jobs SET state='unknown',encrypted_request='',lease_token='',cancel_requested=TRUE,message='The runtime deadline expired; inspect the target before retrying.',updated_at=? WHERE state IN ('pending','running') AND expires_at<=?`), stamp(now), stamp(now))
	return err
}

func (s *SQLStore) ActiveRuntimeMutation(ctx context.Context, appID string) (*core.RuntimeJob, error) {
	j, err := scanRuntimeJob(s.db.QueryRowContext(ctx, s.q(`SELECT `+runtimeJobColumns+` FROM runtime_jobs WHERE app_id=? AND operation NOT IN ('inspect','logs') AND state IN ('pending','running','unknown') LIMIT 1`), appID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return &j, err
}

// Acknowledgment retains the unknown result but permits a new operation only
// after an inspection completed after the uncertainty was recorded. A newly
// enrolled identity may reconcile the same target after the old lease expires;
// it cannot read or complete the old identity's execution request.
func (s *SQLStore) AcknowledgeRuntimeJob(ctx context.Context, app, id, inspection string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE runtime_jobs SET state='acknowledged',updated_at=? WHERE id=? AND app_id=? AND state='unknown' AND lease_until<=? AND EXISTS (SELECT 1 FROM runtime_jobs i WHERE i.id=? AND i.app_id=runtime_jobs.app_id AND i.server_id=runtime_jobs.server_id AND i.node_id=runtime_jobs.node_id AND i.project_id=runtime_jobs.project_id AND i.node_generation>=runtime_jobs.node_generation AND i.operation='inspect' AND i.state='succeeded' AND i.created_at>runtime_jobs.updated_at AND EXISTS (SELECT 1 FROM edge_node_credentials c WHERE c.network_id=i.node_id AND c.generation=i.node_generation AND c.revoked=FALSE AND c.public_key<>'')) AND EXISTS (SELECT 1 FROM apps a WHERE a.id=runtime_jobs.app_id AND a.project_id=runtime_jobs.project_id AND a.server_id=runtime_jobs.server_id) AND EXISTS (SELECT 1 FROM servers s WHERE s.id=runtime_jobs.server_id AND s.agent_node_id=runtime_jobs.node_id)`), stamp(now), id, app, stamp(now), inspection)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrRuntimeJobConflict
	}
	return err
}
