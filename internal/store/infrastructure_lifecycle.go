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

var ErrInfrastructureChanged = errors.New("infrastructure intent, review or operation changed")
var ErrInfrastructureProtected = errors.New("server has applications, services, runtime work or protected storage")

type InfrastructureAdmission func(context.Context, *sql.Tx, core.InfrastructureAcceptance) error

type InfrastructureLifecycleStore interface {
	CreateInfrastructureReview(context.Context, core.InfrastructureReview) error
	GetInfrastructureReview(context.Context, string) (core.InfrastructureReview, error)
	AcceptInfrastructureReview(context.Context, string, string, string, string, time.Time, InfrastructureAdmission) (core.ManagedServer, core.InfrastructureOperation, error)
	GetManagedServer(context.Context, string) (core.ManagedServer, error)
	ListManagedServers(context.Context) ([]core.ManagedServer, error)
	UpdateManagedServer(context.Context, core.ManagedServer, int64) error
	GetInfrastructureOperation(context.Context, string) (core.InfrastructureOperation, error)
	ListInfrastructureOperations(context.Context, string) ([]core.InfrastructureOperation, error)
	LeaseInfrastructureOperation(context.Context, time.Time, time.Duration) (*core.InfrastructureOperation, error)
	CheckpointInfrastructureOperation(context.Context, core.InfrastructureOperation, bool, time.Time) error
	CompleteInfrastructureOperation(context.Context, core.ManagedServer, core.InfrastructureOperation, time.Time, InfrastructureAdmission) error
	CancelInfrastructureOperation(context.Context, string, time.Time) error
	RetryInfrastructureOperation(context.Context, string, time.Time) error
	CreateInfrastructureDeletion(context.Context, core.ManagedServer, core.InfrastructureOperation, time.Time, InfrastructureAdmission) error
	InfrastructureDeletionBlocked(context.Context, string) error
	AdoptInfrastructureServer(context.Context, core.ManagedServer, string, string, time.Time) (core.ManagedServer, error)
}

const reviewColumns = `id,server_id,project_id,provider_id,provider_revision,manifest_digest,name,input,encrypted_request,digest,state,expires_at,created_at`

func (s *SQLStore) CreateInfrastructureReview(ctx context.Context, r core.InfrastructureReview) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO infrastructure_reviews(`+reviewColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`), r.ID, r.ServerID, r.ProjectID, r.ProviderID, r.ProviderRevision, r.ManifestDigest, r.Name, string(r.Input), r.EncryptedRequest, r.Digest, r.State, stamp(r.ExpiresAt), stamp(r.CreatedAt))
	return err
}
func scanInfrastructureReview(row scanner) (core.InfrastructureReview, error) {
	var r core.InfrastructureReview
	var input, expires, created string
	err := row.Scan(&r.ID, &r.ServerID, &r.ProjectID, &r.ProviderID, &r.ProviderRevision, &r.ManifestDigest, &r.Name, &input, &r.EncryptedRequest, &r.Digest, &r.State, &expires, &created)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	r.Input = []byte(input)
	r.ExpiresAt = parseTime(expires)
	r.CreatedAt = parseTime(created)
	return r, err
}
func (s *SQLStore) GetInfrastructureReview(ctx context.Context, id string) (core.InfrastructureReview, error) {
	return scanInfrastructureReview(s.db.QueryRowContext(ctx, s.q(`SELECT `+reviewColumns+` FROM infrastructure_reviews WHERE id=?`), id))
}

const managedServerColumns = `id,review_id,project_id,provider_id,name,node_id,resource_id,address,allocation_state,enrollment_state,runtime_state,revision,created_at,updated_at`

func scanManagedServer(row scanner) (core.ManagedServer, error) {
	var m core.ManagedServer
	var created, updated string
	err := row.Scan(&m.ID, &m.ReviewID, &m.ProjectID, &m.ProviderID, &m.Name, &m.NodeID, &m.ResourceID, &m.Address, &m.AllocationState, &m.EnrollmentState, &m.RuntimeState, &m.Revision, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	m.CreatedAt = parseTime(created)
	m.UpdatedAt = parseTime(updated)
	return m, err
}
func (s *SQLStore) GetManagedServer(ctx context.Context, id string) (core.ManagedServer, error) {
	return scanManagedServer(s.db.QueryRowContext(ctx, s.q(`SELECT `+managedServerColumns+` FROM managed_servers WHERE id=?`), id))
}
func (s *SQLStore) ListManagedServers(ctx context.Context) ([]core.ManagedServer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+managedServerColumns+` FROM managed_servers ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.ManagedServer{}
	for rows.Next() {
		item, err := scanManagedServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *SQLStore) UpdateManagedServer(ctx context.Context, m core.ManagedServer, expected int64) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE managed_servers SET resource_id=?,address=?,allocation_state=?,enrollment_state=?,runtime_state=?,revision=?,updated_at=? WHERE id=? AND project_id=? AND provider_id=? AND review_id=? AND node_id=? AND revision=?`), m.ResourceID, m.Address, m.AllocationState, m.EnrollmentState, m.RuntimeState, m.Revision, stamp(m.UpdatedAt), m.ID, m.ProjectID, m.ProviderID, m.ReviewID, m.NodeID, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrInfrastructureChanged
	}
	return err
}

const infrastructureOperationColumns = `id,server_id,provider_id,actor_id,action,state,stage,provider_operation_id,resource_id,error_code,message,attempts,cancel_requested,expires_at,next_attempt_at,lease_token,lease_until,request_digest,created_at,updated_at`

func scanInfrastructureOperation(row scanner) (core.InfrastructureOperation, error) {
	var o core.InfrastructureOperation
	var expires, next, lease, created, updated string
	err := row.Scan(&o.ID, &o.ServerID, &o.ProviderID, &o.ActorID, &o.Action, &o.State, &o.Stage, &o.ProviderOperationID, &o.ResourceID, &o.ErrorCode, &o.Message, &o.Attempts, &o.CancelRequested, &expires, &next, &o.LeaseToken, &lease, &o.RequestDigest, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	o.ExpiresAt = parseTime(expires)
	o.NextAttemptAt = parseTime(next)
	o.LeaseUntil = parseTime(lease)
	o.CreatedAt = parseTime(created)
	o.UpdatedAt = parseTime(updated)
	return o, err
}
func (s *SQLStore) GetInfrastructureOperation(ctx context.Context, id string) (core.InfrastructureOperation, error) {
	return scanInfrastructureOperation(s.db.QueryRowContext(ctx, s.q(`SELECT `+infrastructureOperationColumns+` FROM infrastructure_operations WHERE id=?`), id))
}
func (s *SQLStore) ListInfrastructureOperations(ctx context.Context, server string) ([]core.InfrastructureOperation, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT `+infrastructureOperationColumns+` FROM infrastructure_operations WHERE server_id=? ORDER BY created_at,id`), server)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.InfrastructureOperation{}
	for rows.Next() {
		o, err := scanInfrastructureOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func (s *SQLStore) insertInfrastructureOperation(ctx context.Context, tx *sql.Tx, o core.InfrastructureOperation) error {
	_, err := tx.ExecContext(ctx, s.q(`INSERT INTO infrastructure_operations(`+infrastructureOperationColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), o.ID, o.ServerID, o.ProviderID, o.ActorID, o.Action, o.State, o.Stage, o.ProviderOperationID, o.ResourceID, o.ErrorCode, o.Message, o.Attempts, o.CancelRequested, stamp(o.ExpiresAt), stamp(o.NextAttemptAt), o.LeaseToken, stamp(o.LeaseUntil), o.RequestDigest, stamp(o.CreatedAt), stamp(o.UpdatedAt))
	return err
}

func (s *SQLStore) AcceptInfrastructureReview(ctx context.Context, reviewID, digest, operationID, actor string, now time.Time, admission InfrastructureAdmission) (resultServer core.ManagedServer, resultOperation core.InfrastructureOperation, resultErr error) {
	defer func() {
		if resultErr != nil {
			old, e := s.GetInfrastructureOperation(ctx, operationID)
			if e == nil && old.Action == "create" && old.RequestDigest == digest && old.ActorID == actor {
				managed, e := s.GetManagedServer(ctx, old.ServerID)
				if e == nil && managed.ReviewID == reviewID {
					resultServer, resultOperation, resultErr = managed, old, nil
				}
			}
		}
	}()
	if old, err := s.GetInfrastructureOperation(ctx, operationID); err == nil {
		m, e := s.GetManagedServer(ctx, old.ServerID)
		if e != nil || old.Action != "create" || m.ReviewID != reviewID || old.RequestDigest != digest || old.ActorID != actor {
			return m, old, ErrInfrastructureChanged
		}
		return m, old, nil
	} else if !errors.Is(err, ErrNotFound) {
		return core.ManagedServer{}, core.InfrastructureOperation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ManagedServer{}, core.InfrastructureOperation{}, err
	}
	defer tx.Rollback()
	query := `SELECT ` + reviewColumns + ` FROM infrastructure_reviews WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	r, err := scanInfrastructureReview(tx.QueryRowContext(ctx, s.q(query), reviewID))
	if err != nil {
		return core.ManagedServer{}, core.InfrastructureOperation{}, err
	}
	m := core.ManagedServer{ID: r.ServerID, ReviewID: r.ID, ProjectID: r.ProjectID, ProviderID: r.ProviderID, Name: r.Name, NodeID: "node-" + r.ServerID, AllocationState: "pending", EnrollmentState: "waiting", RuntimeState: "waiting", Revision: 1, CreatedAt: now, UpdatedAt: now}
	o := core.InfrastructureOperation{ID: operationID, ServerID: m.ID, ProviderID: m.ProviderID, ActorID: actor, Action: "create", State: "pending", Stage: "submit", ExpiresAt: now.Add(30 * time.Minute), NextAttemptAt: now, RequestDigest: digest, CreatedAt: now, UpdatedAt: now}
	if r.Digest != digest || r.State != "open" || !r.ExpiresAt.After(now) {
		return m, o, ErrInfrastructureChanged
	}
	result, err := tx.ExecContext(ctx, s.q(`UPDATE infrastructure_reviews SET state='accepted' WHERE id=? AND state='open' AND EXISTS (SELECT 1 FROM infrastructure_providers p WHERE p.id=infrastructure_reviews.provider_id AND p.revision=infrastructure_reviews.provider_revision AND p.manifest_digest=infrastructure_reviews.manifest_digest AND p.enabled=TRUE AND p.state='ready')`), r.ID)
	if err != nil {
		return m, o, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return m, o, ErrInfrastructureChanged
	}
	if admission != nil {
		if err = admission(ctx, tx.Tx, core.InfrastructureAcceptance{ActorID: actor, ProjectID: m.ProjectID, ProviderID: m.ProviderID, OperationID: o.ID, ServerID: m.ID, Action: "server.create", DesiredConfig: r.Input}); err != nil {
			return m, o, err
		}
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO managed_servers(`+managedServerColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), m.ID, m.ReviewID, m.ProjectID, m.ProviderID, m.Name, m.NodeID, "", "", m.AllocationState, m.EnrollmentState, m.RuntimeState, m.Revision, stamp(now), stamp(now))
	if err != nil {
		return m, o, err
	}
	if err = s.insertInfrastructureOperation(ctx, tx.Tx, o); err != nil {
		return m, o, err
	}
	if err = s.BindMutationAcceptance(ctx, tx.Tx, "infrastructure_operation", o.ID); err != nil {
		return m, o, err
	}
	return m, o, tx.Commit()
}

func (s *SQLStore) LeaseInfrastructureOperation(ctx context.Context, now time.Time, duration time.Duration) (*core.InfrastructureOperation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := `SELECT ` + infrastructureOperationColumns + ` FROM infrastructure_operations WHERE ((state IN ('pending','running') AND next_attempt_at<=?) OR (state='paused' AND expires_at<=?)) AND lease_until<=? ORDER BY created_at,id LIMIT 1`
	if s.postgres {
		query += ` FOR UPDATE SKIP LOCKED`
	}
	o, err := scanInfrastructureOperation(tx.QueryRowContext(ctx, s.q(query), stamp(now), stamp(now), stamp(now)))
	if errors.Is(err, ErrNotFound) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	o.LeaseToken = ulid.Make().String()
	o.LeaseUntil = now.Add(duration)
	o.UpdatedAt = now
	result, err := tx.ExecContext(ctx, s.q(`UPDATE infrastructure_operations SET lease_token=?,lease_until=?,updated_at=?,state=CASE WHEN state='paused' THEN 'pending' ELSE state END WHERE id=? AND lease_until<=? AND state IN ('pending','running','paused')`), o.LeaseToken, stamp(o.LeaseUntil), stamp(now), o.ID, stamp(now))
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return nil, ErrInfrastructureChanged
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &o, nil
}

// Checkpoints never overwrite a cancellation that arrived during provider I/O.
func (s *SQLStore) CheckpointInfrastructureOperation(ctx context.Context, o core.InfrastructureOperation, release bool, now time.Time) error {
	nextToken, nextLease := o.LeaseToken, o.LeaseUntil
	if release {
		nextToken = ""
		nextLease = time.Time{}
	}
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE infrastructure_operations SET state=?,stage=?,provider_operation_id=?,resource_id=?,error_code=?,message=?,attempts=?,next_attempt_at=?,lease_token=?,lease_until=?,updated_at=? WHERE id=? AND server_id=? AND provider_id=? AND lease_token=? AND lease_until>? AND state IN ('pending','running')`), o.State, o.Stage, o.ProviderOperationID, o.ResourceID, o.ErrorCode, o.Message, o.Attempts, stamp(o.NextAttemptAt), nextToken, stamp(nextLease), stamp(now), o.ID, o.ServerID, o.ProviderID, o.LeaseToken, stamp(now))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrInfrastructureChanged
	}
	return err
}
func (s *SQLStore) CancelInfrastructureOperation(ctx context.Context, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE infrastructure_operations SET cancel_requested=TRUE,state=CASE WHEN state='paused' THEN 'pending' ELSE state END,next_attempt_at=?,updated_at=? WHERE id=? AND action='create' AND state IN ('pending','running','paused','unknown')`), stamp(now), stamp(now), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrInfrastructureChanged
	}
	return err
}
func (s *SQLStore) RetryInfrastructureOperation(ctx context.Context, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE infrastructure_operations SET state='pending',attempts=0,next_attempt_at=?,updated_at=? WHERE id=? AND state='paused' AND error_code='transport' AND cancel_requested=FALSE AND expires_at>? AND lease_until<=?`), stamp(now), stamp(now), id, stamp(now), stamp(now))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrInfrastructureChanged
	}
	return err
}

func (s *SQLStore) InfrastructureDeletionBlocked(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return s.infrastructureDeletionBlocked(ctx, tx.Tx, id)
}
func (s *SQLStore) infrastructureDeletionBlocked(ctx context.Context, tx *sql.Tx, id string) error {
	var count int
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM apps WHERE server_id=?`), id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrInfrastructureProtected
	}
	rows, err := tx.QueryContext(ctx, s.q(`SELECT payload FROM storage_resources WHERE server_id=?`), id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw string
		var item core.StorageResource
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal([]byte(raw), &item); err != nil {
			rows.Close()
			return err
		}
		if item.State != "absent" && !item.Independent {
			rows.Close()
			return ErrStorageProtected
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT payload FROM services`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		service, e := decodeService(raw)
		if e != nil {
			rows.Close()
			return e
		}
		if service.ProvisionTarget != nil && service.ProvisionTarget.ServerID == id {
			rows.Close()
			return ErrInfrastructureProtected
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM runtime_jobs WHERE server_id=? AND state IN ('pending','running','unknown')`), id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrInfrastructureProtected
	}
	return nil
}
func (s *SQLStore) CreateInfrastructureDeletion(ctx context.Context, m core.ManagedServer, o core.InfrastructureOperation, now time.Time, admission InfrastructureAdmission) error {
	if old, err := s.GetInfrastructureOperation(ctx, o.ID); err == nil {
		if old.ServerID == m.ID && old.Action == "delete" && old.RequestDigest == o.RequestDigest && old.ActorID == o.ActorID {
			return nil
		}
		return ErrInfrastructureChanged
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := s.InfrastructureDeletionBlocked(ctx, m.ID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.postgres {
		var id string
		if err = tx.QueryRowContext(ctx, s.q(`SELECT id FROM managed_servers WHERE id=? FOR UPDATE`), m.ID).Scan(&id); err != nil {
			return err
		}
	}
	if err = s.infrastructureDeletionBlocked(ctx, tx.Tx, m.ID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.q(`UPDATE managed_servers SET allocation_state='deleting',revision=revision+1,updated_at=? WHERE id=? AND revision=? AND resource_id=? AND allocation_state IN ('allocated','unknown','failed') AND NOT EXISTS (SELECT 1 FROM apps WHERE server_id=managed_servers.id)`), stamp(now), m.ID, m.Revision, m.ResourceID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrInfrastructureChanged
	}
	if admission != nil {
		if err = admission(ctx, tx.Tx, core.InfrastructureAcceptance{ActorID: o.ActorID, ProjectID: m.ProjectID, ProviderID: m.ProviderID, OperationID: o.ID, ServerID: m.ID, Action: "server.delete"}); err != nil {
			return err
		}
	}
	if err = s.insertInfrastructureOperation(ctx, tx.Tx, o); err != nil {
		return err
	}
	if err = s.BindMutationAcceptance(ctx, tx.Tx, "infrastructure_operation", o.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) AdoptInfrastructureServer(ctx context.Context, m core.ManagedServer, resource, address string, now time.Time) (core.ManagedServer, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return m, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE infrastructure_operations SET state='adopted',message='Owned resource inspected and adopted.',updated_at=? WHERE server_id=? AND state='unknown' AND lease_until<=? AND (resource_id='' OR resource_id=?)`), stamp(now), m.ID, stamp(now), resource)
	if err != nil {
		return m, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return m, ErrInfrastructureChanged
	}
	result, err = tx.ExecContext(ctx, s.q(`UPDATE managed_servers SET resource_id=?,address=?,allocation_state='allocated',revision=revision+1,updated_at=? WHERE id=? AND project_id=? AND provider_id=? AND revision=? AND (resource_id='' OR resource_id=?)`), resource, address, stamp(now), m.ID, m.ProjectID, m.ProviderID, m.Revision, resource)
	if err != nil {
		return m, err
	}
	n, err = result.RowsAffected()
	if err != nil || n != 1 {
		return m, ErrInfrastructureChanged
	}
	// Adoption reconciles the original uncertain operation. Preserve that verified
	// outcome on its receipt instead of leaving retries permanently unresolved.
	if _, err = tx.ExecContext(ctx, s.q(`UPDATE public_mutation_receipts SET state='succeeded',message='Owned resource inspected and adopted.',updated_at=? WHERE operation_kind='infrastructure_operation' AND failure_status=0 AND state<>'reserved' AND operation_id IN (SELECT id FROM infrastructure_operations WHERE server_id=? AND state='adopted')`), stamp(now), m.ID); err != nil {
		return m, err
	}
	if err = tx.Commit(); err != nil {
		return m, err
	}
	return s.GetManagedServer(ctx, m.ID)
}

// CompleteInfrastructureOperation commits terminal evidence and reservation
// release together. An expired or replaced lease cannot release ownership.
func (s *SQLStore) CompleteInfrastructureOperation(ctx context.Context, m core.ManagedServer, o core.InfrastructureOperation, now time.Time, admission InfrastructureAdmission) error {
	if o.State != "succeeded" && o.State != "cancelled" && o.State != "unknown" {
		return ErrInfrastructureChanged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE infrastructure_operations SET state=?,stage=?,provider_operation_id=?,resource_id=?,error_code=?,message=?,lease_token='',lease_until=?,updated_at=? WHERE id=? AND server_id=? AND provider_id=? AND lease_token=? AND lease_until>? AND state IN ('pending','running')`), o.State, o.Stage, o.ProviderOperationID, o.ResourceID, o.ErrorCode, o.Message, stamp(time.Time{}), stamp(now), o.ID, m.ID, m.ProviderID, o.LeaseToken, stamp(now))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrInfrastructureChanged
	}
	result, err = tx.ExecContext(ctx, s.q(`UPDATE managed_servers SET resource_id=?,address=?,allocation_state=?,enrollment_state=?,runtime_state=?,revision=?,updated_at=? WHERE id=? AND project_id=? AND provider_id=? AND revision=?`), m.ResourceID, m.Address, m.AllocationState, m.EnrollmentState, m.RuntimeState, m.Revision, stamp(now), m.ID, m.ProjectID, m.ProviderID, m.Revision-1)
	if err != nil {
		return err
	}
	n, err = result.RowsAffected()
	if err != nil || n != 1 {
		return ErrInfrastructureChanged
	}
	action := "server.created"
	if o.Action == "delete" {
		action = "server.deleted"
	}
	if o.State == "cancelled" {
		action = "server.cancelled"
	}
	if o.State == "unknown" {
		action = "server.unresolved"
	}
	if admission != nil {
		if err = admission(ctx, tx.Tx, core.InfrastructureAcceptance{ActorID: o.ActorID, ProjectID: m.ProjectID, ProviderID: m.ProviderID, OperationID: o.ID, ServerID: m.ID, Action: action, ResourceID: m.ResourceID}); err != nil {
			return err
		}
	}
	if err := s.UpdateMutationOutcome(ctx, tx.Tx, "infrastructure_operation", o.ID, o.State); err != nil {
		return err
	}
	return tx.Commit()
}
