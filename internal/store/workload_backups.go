package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type WorkloadBackupStore interface {
	CreateWorkloadBackup(context.Context, core.WorkloadBackup, core.WorkloadBackupOperation) error
	GetWorkloadBackup(context.Context, string) (core.WorkloadBackup, error)
	ListWorkloadBackups(context.Context, string) ([]core.WorkloadBackup, error)
	CreateWorkloadBackupOperation(context.Context, core.WorkloadBackupOperation, int64) error
	GetWorkloadBackupOperation(context.Context, string) (core.WorkloadBackupOperation, error)
	ListWorkloadBackupOperations(context.Context, string) ([]core.WorkloadBackupOperation, error)
	CompleteWorkloadBackupOperation(context.Context, core.WorkloadBackup, core.WorkloadBackupOperation) error
	ClaimWorkloadBackupRecovery(context.Context, string, time.Time, string) (core.WorkloadBackupOperation, error)
}

var ErrWorkloadBackupChanged = errors.New("backup, destination, or active operation changed; inspect and review again")

func scanWorkloadBackup(row scanner) (core.WorkloadBackup, error) {
	var b core.WorkloadBackup
	var raw string
	err := row.Scan(&raw, &b.EncryptedInput, &b.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &b)
	}
	return b, err
}
func scanWorkloadBackupOperation(row scanner) (core.WorkloadBackupOperation, error) {
	var o core.WorkloadBackupOperation
	var raw, lease string
	err := row.Scan(&raw, &o.EncryptedInput, &o.LeaseToken, &lease, &o.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &o)
	}
	o.LeaseUntil = parseTime(lease)
	return o, err
}
func (s *SQLStore) GetWorkloadBackup(ctx context.Context, id string) (core.WorkloadBackup, error) {
	return scanWorkloadBackup(s.db.QueryRowContext(ctx, s.q(`SELECT payload,input_cipher,revision FROM workload_backups WHERE id=?`), id))
}
func (s *SQLStore) GetWorkloadBackupOperation(ctx context.Context, id string) (core.WorkloadBackupOperation, error) {
	return scanWorkloadBackupOperation(s.db.QueryRowContext(ctx, s.q(`SELECT payload,input_cipher,lease_token,lease_until,revision FROM workload_backup_operations WHERE id=?`), id))
}
func (s *SQLStore) ListWorkloadBackups(ctx context.Context, project string) ([]core.WorkloadBackup, error) {
	q := `SELECT payload,input_cipher,revision FROM workload_backups`
	args := []any{}
	if project != "" {
		q += ` WHERE project_id=?`
		args = append(args, project)
	}
	rows, err := s.db.QueryContext(ctx, s.q(q+` ORDER BY created_at DESC,id`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.WorkloadBackup{}
	for rows.Next() {
		b, err := scanWorkloadBackup(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	return items, rows.Err()
}
func (s *SQLStore) ListWorkloadBackupOperations(ctx context.Context, id string) ([]core.WorkloadBackupOperation, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT payload,input_cipher,lease_token,lease_until,revision FROM workload_backup_operations WHERE backup_id=? ORDER BY created_at DESC,id`), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.WorkloadBackupOperation{}
	for rows.Next() {
		o, err := scanWorkloadBackupOperation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, o)
	}
	return items, rows.Err()
}
func (s *SQLStore) insertWorkloadBackupOperation(ctx context.Context, tx *sql.Tx, b core.WorkloadBackup, o core.WorkloadBackupOperation) error {
	if o.BackupID != b.ID || o.ProjectID != b.ProjectID || o.EncryptedInput == "" || o.LeaseToken == "" || o.Revision != 1 || o.State != "running" || !o.LeaseUntil.After(time.Now()) {
		return ErrWorkloadBackupChanged
	}
	var busy int
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workload_backup_operations WHERE backup_id=? AND state IN ('running','unknown')`), b.ID).Scan(&busy); err != nil {
		return err
	}
	if busy > 0 {
		return ErrWorkloadBackupChanged
	}
	if o.Action == "backup" || o.Action == "restore" {
		target := b.SourceRunID
		if o.Action == "restore" {
			target = o.TargetRunID
		}
		q := `SELECT ` + serviceResourceColumns + ` FROM service_resources WHERE run_id=?`
		if s.postgres {
			q += ` FOR UPDATE`
		}
		resource, err := scanServiceResource(tx.QueryRowContext(ctx, s.q(q), target))
		if err != nil || resource.ProjectID != b.ProjectID || resource.Target.ServerID != b.ServerID || resource.State != "ready" || resource.LeaseUntil.After(time.Now()) {
			return ErrWorkloadBackupChanged
		}
		var active int
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workload_backup_operations WHERE state IN ('running','unknown') AND (target_run_id=? OR (source_run_id=? AND action='backup'))`), target, target).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrWorkloadBackupChanged
		}
		if o.Action == "restore" {
			if o.TargetResourceID != resource.ResourceID {
				return ErrWorkloadBackupChanged
			}
			var ignored string
			err = tx.QueryRowContext(ctx, s.q(s.serviceLockQuery()), resource.ServiceID).Scan(&ignored)
			if err != nil {
				return ErrWorkloadBackupChanged
			}
			used, err := s.serviceResourceConsumers(ctx, tx, resource.ServiceID)
			if err != nil {
				return err
			}
			if used {
				return ErrServiceInUse
			}
		}
	}
	_, err := tx.ExecContext(ctx, s.q(`INSERT INTO workload_backup_operations(id,backup_id,project_id,source_run_id,target_run_id,action,state,revision,lease_token,lease_until,input_cipher,payload,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`), o.ID, b.ID, b.ProjectID, b.SourceRunID, o.TargetRunID, o.Action, o.State, o.Revision, o.LeaseToken, stamp(o.LeaseUntil), o.EncryptedInput, jsonText(o), stamp(o.CreatedAt))
	if err != nil {
		return err
	}
	return s.BindMutationAcceptance(ctx, tx, "workload_backup", o.ID)
}
func (s *SQLStore) CreateWorkloadBackup(ctx context.Context, b core.WorkloadBackup, o core.WorkloadBackupOperation) error {
	if b.EncryptedInput == "" || b.Revision != 1 || b.Policy != "retain" || b.State != "creating" || b.Location != "target-local" || o.Action != "backup" {
		return ErrWorkloadBackupChanged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO workload_backups(id,project_id,server_id,source_run_id,state,revision,input_cipher,payload,created_at) VALUES(?,?,?,?,?,?,?,?,?)`), b.ID, b.ProjectID, b.ServerID, b.SourceRunID, b.State, b.Revision, b.EncryptedInput, jsonText(b), stamp(b.CreatedAt))
	if err != nil {
		return err
	}
	if err = s.insertWorkloadBackupOperation(ctx, tx.Tx, b, o); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLStore) CreateWorkloadBackupOperation(ctx context.Context, o core.WorkloadBackupOperation, revision int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := `SELECT payload,input_cipher,revision FROM workload_backups WHERE id=?`
	if s.postgres {
		q += ` FOR UPDATE`
	}
	b, err := scanWorkloadBackup(tx.QueryRowContext(ctx, s.q(q), o.BackupID))
	if err != nil {
		return err
	}
	if b.Revision != revision || b.State == "deleted" || (o.Action == "verify" || o.Action == "restore") && b.State != "ready" {
		return ErrWorkloadBackupChanged
	}
	if err = s.insertWorkloadBackupOperation(ctx, tx.Tx, b, o); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLStore) CompleteWorkloadBackupOperation(ctx context.Context, b core.WorkloadBackup, o core.WorkloadBackupOperation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	before := o.Revision
	o.Revision++
	o.UpdatedAt = time.Now().UTC()
	lease := time.Time{}
	if o.State == "unknown" {
		lease = o.LeaseUntil
	}
	result, err := tx.ExecContext(ctx, s.q(`UPDATE workload_backup_operations SET state=?,revision=?,lease_token='',lease_until=?,payload=? WHERE id=? AND backup_id=? AND revision=? AND lease_token=?`), o.State, o.Revision, stamp(lease), jsonText(o), o.ID, b.ID, before, o.LeaseToken)
	if err = changed(result, err); err != nil {
		return err
	}
	before = b.Revision
	b.Revision++
	b.UpdatedAt = o.UpdatedAt
	result, err = tx.ExecContext(ctx, s.q(`UPDATE workload_backups SET state=?,revision=?,payload=? WHERE id=? AND revision=?`), b.State, b.Revision, jsonText(b), b.ID, before)
	if err = changed(result, err); err != nil {
		return err
	}
	if err = s.UpdateMutationOutcome(ctx, tx.Tx, "workload_backup", o.ID, o.State); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLStore) ClaimWorkloadBackupRecovery(ctx context.Context, id string, now time.Time, token string) (core.WorkloadBackupOperation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.WorkloadBackupOperation{}, err
	}
	defer tx.Rollback()
	q := `SELECT payload,input_cipher,lease_token,lease_until,revision FROM workload_backup_operations WHERE id=?`
	if s.postgres {
		q += ` FOR UPDATE`
	}
	o, err := scanWorkloadBackupOperation(tx.QueryRowContext(ctx, s.q(q), id))
	if err != nil {
		return o, err
	}
	if o.LeaseUntil.After(now) || (o.State != "running" && o.State != "unknown") {
		return o, ErrWorkloadBackupChanged
	}
	before := o.Revision
	o.Revision++
	o.LeaseToken = token
	o.LeaseUntil = now.Add(31 * time.Minute)
	o.UpdatedAt = now
	o.State = "running"
	result, err := tx.ExecContext(ctx, s.q(`UPDATE workload_backup_operations SET state=?,revision=?,lease_token=?,lease_until=?,payload=? WHERE id=? AND revision=?`), o.State, o.Revision, token, stamp(o.LeaseUntil), jsonText(o), o.ID, before)
	if err = changed(result, err); err != nil {
		return o, err
	}
	return o, tx.Commit()
}

// Reconciliation acknowledges only expired uncertain data operations on the same current agent.
func (s *SQLStore) ReconcileWorkloadBackupRuntime(ctx context.Context, run, operation, inspection string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE runtime_jobs SET state='acknowledged',updated_at=? WHERE service_run_id=? AND id=? AND operation='workload_backup' AND state='unknown' AND lease_until<=? AND EXISTS (SELECT 1 FROM runtime_jobs i WHERE i.id=? AND i.service_run_id=runtime_jobs.service_run_id AND i.server_id=runtime_jobs.server_id AND i.node_id=runtime_jobs.node_id AND i.node_generation>=runtime_jobs.node_generation AND i.operation='workload_backup_inspect' AND i.state='succeeded' AND i.created_at>runtime_jobs.updated_at AND i.created_at>runtime_jobs.lease_until AND EXISTS (SELECT 1 FROM edge_node_credentials c WHERE c.network_id=i.node_id AND c.generation=i.node_generation AND c.revoked=FALSE AND c.public_key<>'')) AND EXISTS (SELECT 1 FROM servers s WHERE s.id=runtime_jobs.server_id AND s.agent_node_id=runtime_jobs.node_id)`), stamp(now), run, "backup-"+operation, stamp(now), inspection)
	if err != nil {
		return err
	}
	var n int
	err = s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM runtime_jobs WHERE service_run_id=? AND operation='workload_backup' AND state IN ('pending','running','unknown')`), run).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrRuntimeJobConflict
	}
	return nil
}
