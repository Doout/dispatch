package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

type InfrastructureActionStore interface {
	RetryInfrastructureAction(context.Context, string, time.Time) error
	LeaseInfrastructureActionResolution(context.Context, string, time.Time) (*core.InfrastructureOperation, error)
	AcceptInfrastructureAction(context.Context, core.ManagedServer, core.InfrastructureOperation, core.InfrastructureActionRequest, time.Time, InfrastructureAdmission) (core.ManagedServer, core.InfrastructureOperation, error)
	GetInfrastructureActionRequest(context.Context, string) (core.InfrastructureActionRequest, error)
	CompleteInfrastructureAction(context.Context, core.ManagedServer, core.InfrastructureOperation, core.EdgeCredential, time.Time, InfrastructureAdmission) error
}

func (s *SQLStore) GetInfrastructureActionRequest(ctx context.Context, id string) (core.InfrastructureActionRequest, error) {
	var r core.InfrastructureActionRequest
	err := s.db.QueryRowContext(ctx, s.q(`SELECT operation_id,provider_revision,manifest_digest,encrypted_request,cipher_digest FROM infrastructure_action_requests WHERE operation_id=?`), id).Scan(&r.OperationID, &r.ProviderRevision, &r.ManifestDigest, &r.EncryptedRequest, &r.CipherDigest)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return r, err
}

func (s *SQLStore) AcceptInfrastructureAction(ctx context.Context, m core.ManagedServer, o core.InfrastructureOperation, r core.InfrastructureActionRequest, now time.Time, admission InfrastructureAdmission) (core.ManagedServer, core.InfrastructureOperation, error) {
	if old, err := s.GetInfrastructureOperation(ctx, o.ID); err == nil {
		if old.ActorID != o.ActorID || old.ServerID != m.ID || old.Action != o.Action || old.RequestDigest != o.RequestDigest {
			return m, old, ErrInfrastructureChanged
		}
		current, e := s.GetManagedServer(ctx, m.ID)
		return current, old, e
	} else if !errors.Is(err, ErrNotFound) {
		return m, o, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return m, o, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE managed_servers SET power_state=?,promotion_state=?,runtime_state='waiting',revision=revision+1,updated_at=? WHERE id=? AND revision=? AND allocation_state='allocated' AND resource_id=? AND node_id=? AND EXISTS(SELECT 1 FROM infrastructure_providers p WHERE p.id=managed_servers.provider_id AND p.revision=? AND p.manifest_digest=? AND p.enabled=TRUE AND p.state='ready')`), m.PowerState, m.PromotionState, stamp(now), m.ID, m.Revision, m.ResourceID, m.NodeID, r.ProviderRevision, r.ManifestDigest)
	if err != nil {
		return m, o, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return m, o, ErrInfrastructureChanged
	}
	var count int
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM runtime_jobs WHERE server_id=? AND state IN ('pending','running','unknown')`), m.ID).Scan(&count); err != nil {
		return m, o, err
	}
	if count > 0 {
		return m, o, ErrInfrastructureProtected
	}
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM deployments d JOIN apps a ON a.id=d.app_id WHERE a.server_id=? AND d.state IN ('pending','running','queued')`), m.ID).Scan(&count); err != nil {
		return m, o, err
	}
	if count > 0 {
		return m, o, ErrInfrastructureProtected
	}
	if _, err = tx.ExecContext(ctx, s.q(`UPDATE servers SET state='waiting' WHERE id=? AND agent_node_id=?`), m.ID, m.NodeID); err != nil {
		return m, o, err
	}
	if admission != nil {
		action := "server.power"
		if o.Action == "server.promote" {
			action = "server.promote"
		}
		if err = admission(ctx, tx.Tx, core.InfrastructureAcceptance{ActorID: o.ActorID, ProjectID: m.ProjectID, ProviderID: m.ProviderID, ServerID: m.ID, OperationID: o.ID, Action: action, ResourceID: m.ResourceID}); err != nil {
			return m, o, err
		}
	}
	if err = s.insertInfrastructureOperation(ctx, tx.Tx, o); err != nil {
		return m, o, err
	}
	if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO infrastructure_action_requests(operation_id,provider_revision,manifest_digest,encrypted_request,cipher_digest) VALUES(?,?,?,?,?)`), r.OperationID, r.ProviderRevision, r.ManifestDigest, r.EncryptedRequest, r.CipherDigest); err != nil {
		return m, o, err
	}
	if err = s.BindMutationAcceptance(ctx, tx.Tx, "infrastructure_operation", o.ID); err != nil {
		return m, o, err
	}
	if err = tx.Commit(); err != nil {
		return m, o, err
	}
	m.Revision++
	m.RuntimeState = "waiting"
	m.UpdatedAt = now
	return m, o, nil
}

// Completion never changes allocation ownership or releases its quota. It only
// records the confirmed machine action under the current lease and revision.
func (s *SQLStore) CompleteInfrastructureAction(ctx context.Context, m core.ManagedServer, o core.InfrastructureOperation, expected core.EdgeCredential, now time.Time, admission InfrastructureAdmission) error {
	if o.State != "succeeded" && o.State != "cancelled" && o.State != "unknown" && o.State != "failed" {
		return ErrInfrastructureChanged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if o.Action == "server.promote" && o.State == "succeeded" {
		query := `SELECT generation,public_key,revoked FROM edge_node_credentials WHERE network_id=?`
		if s.postgres {
			query += ` FOR SHARE`
		}
		var generation int64
		var public string
		var revoked bool
		if err = tx.QueryRowContext(ctx, s.q(query), m.NodeID).Scan(&generation, &public, &revoked); err != nil {
			return err
		}
		if revoked || generation != expected.Generation || public == "" || public != expected.PublicKey || expected.NetworkID != m.NodeID {
			return ErrInfrastructureChanged
		}
	}
	result, err := tx.ExecContext(ctx, s.q(`UPDATE infrastructure_operations SET state=?,stage=?,provider_operation_id=?,resource_id=?,error_code=?,message=?,lease_token='',lease_until=?,updated_at=? WHERE id=? AND server_id=? AND provider_id=? AND lease_token=? AND lease_until>? AND state IN ('pending','running')`), o.State, o.Stage, o.ProviderOperationID, o.ResourceID, o.ErrorCode, o.Message, stamp(time.Time{}), stamp(now), o.ID, m.ID, m.ProviderID, o.LeaseToken, stamp(now))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrInfrastructureChanged
	}
	result, err = tx.ExecContext(ctx, s.q(`UPDATE managed_servers SET address=?,power_state=?,power_checked_at=?,network=?,promotion_state=?,promoted_at=?,promotion_evidence=?,runtime_ready_after=?,runtime_state=?,revision=?,updated_at=? WHERE id=? AND revision=? AND resource_id=? AND node_id=? AND allocation_state='allocated'`), m.Address, m.PowerState, stamp(m.PowerCheckedAt), m.Network, m.PromotionState, stamp(m.PromotedAt), string(m.PromotionEvidence), stamp(m.RuntimeReadyAfter), m.RuntimeState, m.Revision, stamp(now), m.ID, m.Revision-1, m.ResourceID, m.NodeID)
	if err != nil {
		return err
	}
	n, err = result.RowsAffected()
	if err != nil || n != 1 {
		return ErrInfrastructureChanged
	}
	if err = s.UpdateMutationOutcome(ctx, tx.Tx, "infrastructure_operation", o.ID, o.State); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) RefreshManagedTargetState(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE servers SET state=CASE WHEN EXISTS(SELECT 1 FROM managed_servers m WHERE m.id=servers.id AND m.allocation_state='allocated' AND m.runtime_state='ready' AND (m.power_state='' OR m.power_state='running') AND (m.source_snapshot_id='' OR m.promotion_state='promoted')) THEN 'ready' ELSE 'waiting' END WHERE id=? AND EXISTS(SELECT 1 FROM managed_servers m WHERE m.id=servers.id AND m.node_id=servers.agent_node_id)`), id)
	return err
}

// Resolution leases only a known provider operation. It cannot authorize a
// new submission, replace the request or reset the original deadline.
func (s *SQLStore) LeaseInfrastructureActionResolution(ctx context.Context, id string, now time.Time) (*core.InfrastructureOperation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := `SELECT ` + infrastructureOperationColumns + ` FROM infrastructure_operations WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	o, err := scanInfrastructureOperation(tx.QueryRowContext(ctx, s.q(query), id))
	if err != nil {
		return nil, err
	}
	if o.State != "unknown" || o.Stage != "poll" || o.ProviderOperationID == "" || o.LeaseUntil.After(now) || (o.Action != "server.start" && o.Action != "server.stop" && o.Action != "server.reboot" && o.Action != "server.promote") {
		return nil, ErrInfrastructureChanged
	}
	o.State = "running"
	o.LeaseToken = ulid.Make().String()
	o.LeaseUntil = now.Add(2 * time.Minute)
	result, err := tx.ExecContext(ctx, s.q(`UPDATE infrastructure_operations SET state='running',lease_token=?,lease_until=?,updated_at=? WHERE id=? AND state='unknown' AND stage='poll' AND provider_operation_id<>'' AND lease_until<=?`), o.LeaseToken, stamp(o.LeaseUntil), stamp(now), id, stamp(now))
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

func (s *SQLStore) RetryInfrastructureAction(ctx context.Context, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE infrastructure_operations SET state='pending',attempts=0,next_attempt_at=?,updated_at=? WHERE id=? AND action IN ('server.start','server.stop','server.reboot','server.promote') AND state='unknown' AND stage IN ('submit','submitting','poll') AND cancel_requested=FALSE AND expires_at>? AND lease_until<=?`), stamp(now), stamp(now), id, stamp(now), stamp(now))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrInfrastructureChanged
	}
	return err
}
