package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

var ErrSnapshotProtected = errors.New("snapshot retention or an unfinished restore protects this snapshot")

type InfrastructureSnapshotStore interface {
	ResolveInfrastructureSnapshot(context.Context, core.InfrastructureSnapshot, string, time.Time) error
	CreateInfrastructureSnapshotReview(context.Context, core.InfrastructureSnapshotReview) error
	GetInfrastructureSnapshotReview(context.Context, string) (core.InfrastructureSnapshotReview, error)
	SnapshotReviewForOperation(context.Context, string) (core.InfrastructureSnapshotReview, error)
	GetInfrastructureSnapshot(context.Context, string) (core.InfrastructureSnapshot, error)
	ListInfrastructureSnapshots(context.Context, string) ([]core.InfrastructureSnapshot, error)
	AcceptInfrastructureSnapshotReview(context.Context, string, string, string, string, time.Time, InfrastructureAdmission) (core.InfrastructureSnapshot, core.InfrastructureOperation, error)
	CompleteInfrastructureSnapshot(context.Context, core.InfrastructureSnapshot, core.InfrastructureOperation, time.Time, InfrastructureAdmission) error
}

func (s *SQLStore) CreateInfrastructureSnapshotReview(ctx context.Context, r core.InfrastructureSnapshotReview) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.q(`INSERT INTO infrastructure_snapshot_reviews(id,snapshot_id,server_id,project_id,provider_id,state,operation_id,record,encrypted_request)VALUES(?,?,?,?,?,?,NULL,?,?)`), r.ID, r.SnapshotID, r.ServerID, r.ProjectID, r.ProviderID, r.State, string(raw), r.EncryptedRequest)
	return err
}
func scanSnapshotReview(row scanner) (core.InfrastructureSnapshotReview, error) {
	var r core.InfrastructureSnapshotReview
	var raw, state string
	var op sql.NullString
	var cipher string
	err := row.Scan(&raw, &cipher, &state, &op)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(raw), &r); err != nil {
		return r, err
	}
	r.EncryptedRequest = cipher
	r.State = state
	r.OperationID = op.String
	return r, nil
}
func (s *SQLStore) GetInfrastructureSnapshotReview(ctx context.Context, id string) (core.InfrastructureSnapshotReview, error) {
	return scanSnapshotReview(s.db.QueryRowContext(ctx, s.q(`SELECT record,encrypted_request,state,operation_id FROM infrastructure_snapshot_reviews WHERE id=?`), id))
}
func (s *SQLStore) SnapshotReviewForOperation(ctx context.Context, id string) (core.InfrastructureSnapshotReview, error) {
	return scanSnapshotReview(s.db.QueryRowContext(ctx, s.q(`SELECT record,encrypted_request,state,operation_id FROM infrastructure_snapshot_reviews WHERE operation_id=?`), id))
}
func scanSnapshot(row scanner) (core.InfrastructureSnapshot, error) {
	var snapshot core.InfrastructureSnapshot
	var raw string
	err := row.Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, ErrNotFound
	}
	if err != nil {
		return snapshot, err
	}
	err = json.Unmarshal([]byte(raw), &snapshot)
	return snapshot, err
}
func (s *SQLStore) GetInfrastructureSnapshot(ctx context.Context, id string) (core.InfrastructureSnapshot, error) {
	return scanSnapshot(s.db.QueryRowContext(ctx, s.q(`SELECT record FROM infrastructure_snapshots WHERE id=?`), id))
}
func (s *SQLStore) ListInfrastructureSnapshots(ctx context.Context, project string) ([]core.InfrastructureSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT record FROM infrastructure_snapshots WHERE project_id=? ORDER BY id`), project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.InfrastructureSnapshot{}
	for rows.Next() {
		snapshot, e := scanSnapshot(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, snapshot)
	}
	return out, rows.Err()
}
func (s *SQLStore) lockRestorableSnapshot(ctx context.Context, tx *sql.Tx, id, project, provider string) error {
	query := `SELECT record FROM infrastructure_snapshots WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	snapshot, err := scanSnapshot(tx.QueryRowContext(ctx, s.q(query), id))
	if err != nil {
		return err
	}
	if snapshot.ProjectID != project || snapshot.ProviderID != provider || snapshot.State != "ready" {
		return ErrInfrastructureChanged
	}
	return nil
}
func (s *SQLStore) AcceptInfrastructureSnapshotReview(ctx context.Context, id, digest, opID, actor string, now time.Time, admission InfrastructureAdmission) (result core.InfrastructureSnapshot, op core.InfrastructureOperation, resultErr error) {
	// A replay may inspect its original operation, but never bind a fresh key to it.
	defer func() {
		if resultErr == nil {
			return
		}
		old, err := s.GetInfrastructureOperation(ctx, opID)
		if err != nil || old.ActorID != actor || old.RequestDigest != digest {
			return
		}
		review, err := s.SnapshotReviewForOperation(ctx, old.ID)
		if err != nil || review.ID != id {
			return
		}
		snapshot, err := s.GetInfrastructureSnapshot(ctx, review.SnapshotID)
		if err == nil {
			result, op, resultErr = snapshot, old, nil
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, op, err
	}
	defer tx.Rollback()
	query := `SELECT record,encrypted_request,state,operation_id FROM infrastructure_snapshot_reviews WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	r, err := scanSnapshotReview(tx.QueryRowContext(ctx, s.q(query), id))
	if err != nil {
		return result, op, err
	}
	if r.State != "open" || r.Digest != digest || !r.ExpiresAt.After(now) {
		return result, op, ErrInfrastructureChanged
	}
	query = `SELECT ` + managedServerColumns + ` FROM managed_servers WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	source, err := scanManagedServer(tx.QueryRowContext(ctx, s.q(query), r.ServerID))
	if err != nil {
		return result, op, err
	}
	if source.ProjectID != r.ProjectID || source.ProviderID != r.ProviderID {
		return result, op, ErrInfrastructureChanged
	}
	var providerOK int
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM infrastructure_providers WHERE id=? AND revision=? AND manifest_digest=? AND enabled=TRUE AND state='ready'`), r.ProviderID, r.ProviderRevision, r.ManifestDigest).Scan(&providerOK); err != nil {
		return result, op, err
	}
	if providerOK != 1 {
		return result, op, ErrInfrastructureChanged
	}
	op = core.InfrastructureOperation{ID: opID, ServerID: r.ServerID, ProviderID: r.ProviderID, ActorID: actor, Action: r.Action, State: "pending", Stage: "submit", ExpiresAt: now.Add(30 * time.Minute), NextAttemptAt: now, RequestDigest: r.Digest, CreatedAt: now, UpdatedAt: now}
	switch r.Action {
	case "snapshot.create":
		if source.AllocationState != "allocated" || source.ResourceID == "" {
			return result, op, ErrInfrastructureChanged
		}
		result = core.InfrastructureSnapshot{ID: r.SnapshotID, ReviewID: r.ID, ServerID: r.ServerID, ProjectID: r.ProjectID, ProviderID: r.ProviderID, Name: r.Name, State: "pending", RetainUntil: r.RetainUntil, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if admission != nil {
			if err = admission(ctx, tx.Tx, core.InfrastructureAcceptance{ActorID: actor, ProjectID: r.ProjectID, ProviderID: r.ProviderID, ServerID: r.ServerID, SnapshotID: r.SnapshotID, OperationID: opID, Action: r.Action}); err != nil {
				return result, op, err
			}
		}
		raw, _ := json.Marshal(result)
		_, err = tx.ExecContext(ctx, s.q(`INSERT INTO infrastructure_snapshots(id,server_id,project_id,provider_id,state,revision,record)VALUES(?,?,?,?,?,?,?)`), result.ID, result.ServerID, result.ProjectID, result.ProviderID, result.State, result.Revision, string(raw))
		if err != nil {
			return result, op, err
		}
	case "snapshot.delete":
		query = `SELECT record FROM infrastructure_snapshots WHERE id=?`
		if s.postgres {
			query += ` FOR UPDATE`
		}
		result, err = scanSnapshot(tx.QueryRowContext(ctx, s.q(query), r.SnapshotID))
		if err != nil {
			return result, op, err
		}
		if result.ProviderID != r.ProviderID || result.ProjectID != r.ProjectID || result.ServerID != r.ServerID || result.Name != r.Name || result.State != "ready" && result.State != "corrupt" && result.State != "failed" {
			return result, op, ErrInfrastructureChanged
		}
		if result.RetainUntil.After(now) {
			return result, op, ErrSnapshotProtected
		}
		var active int
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM managed_servers WHERE source_snapshot_id=? AND allocation_state NOT IN ('allocated','deleted','cancelled')`), result.ID).Scan(&active); err != nil {
			return result, op, err
		}
		if active > 0 {
			return result, op, ErrSnapshotProtected
		}
		if result.ResourceID == "" {
			return result, op, ErrInfrastructureChanged
		}
		result.State = "deleting"
		result.Revision++
		result.UpdatedAt = now
		if err = s.updateSnapshot(ctx, tx.Tx, result, result.Revision-1); err != nil {
			return result, op, err
		}
		op.ResourceID = result.ResourceID
	default:
		return result, op, ErrInfrastructureChanged
	}
	updated, err := tx.ExecContext(ctx, s.q(`UPDATE infrastructure_snapshot_reviews SET state='accepted',operation_id=? WHERE id=? AND state='open'`), opID, r.ID)
	if err != nil {
		return result, op, err
	}
	n, err := updated.RowsAffected()
	if err != nil || n != 1 {
		return result, op, ErrInfrastructureChanged
	}
	if err = s.insertInfrastructureOperation(ctx, tx.Tx, op); err != nil {
		return result, op, err
	}
	if err = s.BindMutationAcceptance(ctx, tx.Tx, "infrastructure_operation", op.ID); err != nil {
		return result, op, err
	}
	return result, op, tx.Commit()
}
func (s *SQLStore) updateSnapshot(ctx context.Context, tx *sql.Tx, snapshot core.InfrastructureSnapshot, expected int64) error {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.q(`UPDATE infrastructure_snapshots SET resource_id=?,state=?,revision=?,record=? WHERE id=? AND project_id=? AND provider_id=? AND server_id=? AND revision=?`), snapshot.ResourceID, snapshot.State, snapshot.Revision, string(raw), snapshot.ID, snapshot.ProjectID, snapshot.ProviderID, snapshot.ServerID, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrInfrastructureChanged
	}
	return err
}
func (s *SQLStore) CompleteInfrastructureSnapshot(ctx context.Context, snapshot core.InfrastructureSnapshot, op core.InfrastructureOperation, now time.Time, admission InfrastructureAdmission) error {
	if op.State != "succeeded" && op.State != "cancelled" && op.State != "unknown" {
		return ErrInfrastructureChanged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE infrastructure_operations SET state=?,stage=?,provider_operation_id=?,resource_id=?,error_code=?,message=?,lease_token='',lease_until=?,updated_at=? WHERE id=? AND server_id=? AND provider_id=? AND action IN ('snapshot.create','snapshot.delete') AND lease_token=? AND lease_until>? AND state IN ('pending','running')`), op.State, op.Stage, op.ProviderOperationID, op.ResourceID, op.ErrorCode, op.Message, stamp(time.Time{}), stamp(now), op.ID, snapshot.ServerID, snapshot.ProviderID, op.LeaseToken, stamp(now))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrInfrastructureChanged
	}
	if err = s.updateSnapshot(ctx, tx.Tx, snapshot, snapshot.Revision-1); err != nil {
		return err
	}
	if admission != nil {
		action := "snapshot.created"
		if op.Action == "snapshot.delete" {
			action = "snapshot.deleted"
		}
		if op.State == "unknown" {
			action = "snapshot.unresolved"
		}
		if op.State == "cancelled" {
			action = "snapshot.cancelled"
		}
		if err = admission(ctx, tx.Tx, core.InfrastructureAcceptance{ActorID: op.ActorID, ProjectID: snapshot.ProjectID, ProviderID: snapshot.ProviderID, ServerID: snapshot.ServerID, SnapshotID: snapshot.ID, OperationID: op.ID, Action: action, ResourceID: snapshot.ResourceID}); err != nil {
			return err
		}
	}
	if err = s.UpdateMutationOutcome(ctx, tx.Tx, "infrastructure_operation", op.ID, op.State); err != nil {
		return err
	}
	return tx.Commit()
}

// ResolveInfrastructureSnapshot records an explicit inspection after the old
// lease has ended. It never submits another provider mutation.
func (s *SQLStore) ResolveInfrastructureSnapshot(ctx context.Context, snapshot core.InfrastructureSnapshot, operationID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state := "adopted"
	if snapshot.State == "deleted" {
		state = "succeeded"
	}
	result, err := tx.ExecContext(ctx, s.q(`UPDATE infrastructure_operations SET state=?,resource_id=?,message='Snapshot disposition verified by explicit inspection.',updated_at=? WHERE id=? AND server_id=? AND provider_id=? AND state='unknown' AND lease_until<=? AND (resource_id='' OR resource_id=?) AND EXISTS(SELECT 1 FROM infrastructure_snapshot_reviews r WHERE r.operation_id=infrastructure_operations.id AND r.snapshot_id=?)`), state, snapshot.ResourceID, stamp(now), operationID, snapshot.ServerID, snapshot.ProviderID, stamp(now), snapshot.ResourceID, snapshot.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrInfrastructureChanged
	}
	snapshot.Revision++
	snapshot.UpdatedAt = now
	if err = s.updateSnapshot(ctx, tx.Tx, snapshot, snapshot.Revision-1); err != nil {
		return err
	}
	if err = s.UpdateMutationOutcome(ctx, tx.Tx, "infrastructure_operation", operationID, "succeeded"); err != nil {
		return err
	}
	return tx.Commit()
}
