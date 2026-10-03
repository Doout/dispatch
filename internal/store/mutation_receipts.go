package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"time"

	"github.com/doout/dispatch/internal/core"
)

var (
	ErrMutationConflict  = errors.New("idempotency key was already used with different inputs")
	ErrMutationExpired   = errors.New("idempotency retry window expired; the original request will not be repeated")
	ErrMutationClaimLost = errors.New("mutation acceptance claim changed; inspect the original receipt")
)

const MutationRetryWindow = 7 * 24 * time.Hour
const MutationClaimDuration = time.Minute

type MutationReceiptStore interface {
	ReserveMutationReceipt(context.Context, core.MutationReceipt, time.Time) (core.MutationReceipt, bool, error)
	GetMutationReceipt(context.Context, string) (core.MutationReceipt, error)
	FailMutationReceipt(context.Context, core.MutationAcceptance, int, string, time.Time) error
}

const mutationColumns = `id,caller_kind,caller_id,credential_id,project_id,action,key_digest,request_digest,operation_kind,operation_id,resource_id,state,message,failure_status,claim_token,claim_until,retry_until,created_at,updated_at`

var mutationDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func scanMutation(row scanner) (core.MutationReceipt, error) {
	var r core.MutationReceipt
	var claim, retry, created, updated string
	err := row.Scan(&r.ID, &r.CallerKind, &r.CallerID, &r.CredentialID, &r.ProjectID, &r.Action, &r.KeyDigest, &r.RequestDigest, &r.OperationKind, &r.OperationID, &r.ResourceID, &r.State, &r.Message, &r.FailureStatus, &r.ClaimToken, &claim, &retry, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	r.ClaimUntil, r.RetryUntil, r.CreatedAt, r.UpdatedAt = parseTime(claim), parseTime(retry), parseTime(created), parseTime(updated)
	return r, err
}
func (s *SQLStore) GetMutationReceipt(ctx context.Context, id string) (core.MutationReceipt, error) {
	return scanMutation(s.db.QueryRowContext(ctx, s.q(`SELECT `+mutationColumns+` FROM public_mutation_receipts WHERE id=?`), id))
}

// Reservation happens before any external effect. Reclaiming a preparation lease
// changes only its token; the preallocated operation identity never changes.
func (s *SQLStore) ReserveMutationReceipt(ctx context.Context, r core.MutationReceipt, now time.Time) (core.MutationReceipt, bool, error) {
	if r.ID == "" || r.CallerKind == "" || r.CallerID == "" || r.Action == "" || !mutationDigest.MatchString(r.KeyDigest) || !mutationDigest.MatchString(r.RequestDigest) || r.OperationID == "" || r.OperationKind == "" || r.ClaimToken == "" {
		return core.MutationReceipt{}, false, errors.New("incomplete mutation receipt")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, false, err
	}
	defer tx.Rollback()
	r.State, r.CreatedAt, r.UpdatedAt, r.ClaimUntil, r.RetryUntil = "reserved", now, now, now.Add(MutationClaimDuration), now.Add(MutationRetryWindow)
	result, err := tx.ExecContext(ctx, s.q(`INSERT INTO public_mutation_receipts(`+mutationColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`), r.ID, r.CallerKind, r.CallerID, r.CredentialID, r.ProjectID, r.Action, r.KeyDigest, r.RequestDigest, r.OperationKind, r.OperationID, r.ResourceID, r.State, "", 0, r.ClaimToken, stamp(r.ClaimUntil), stamp(r.RetryUntil), stamp(now), stamp(now))
	if err != nil {
		return r, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return r, false, err
	}
	query := `SELECT ` + mutationColumns + ` FROM public_mutation_receipts WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	saved, err := scanMutation(tx.QueryRowContext(ctx, s.q(query), r.ID))
	if err != nil {
		return saved, false, err
	}
	if saved.CallerKind != r.CallerKind || saved.CallerID != r.CallerID || saved.ProjectID != r.ProjectID || saved.Action != r.Action || saved.KeyDigest != r.KeyDigest || saved.RequestDigest != r.RequestDigest || saved.OperationKind != r.OperationKind || saved.ResourceID != r.ResourceID {
		return saved, false, ErrMutationConflict
	}
	if !now.Before(saved.RetryUntil) {
		return saved, false, ErrMutationExpired
	}
	claimed := inserted == 1
	if !claimed && saved.State == "reserved" && !saved.ClaimUntil.After(now) {
		_, err = tx.ExecContext(ctx, s.q(`UPDATE public_mutation_receipts SET claim_token=?,claim_until=?,updated_at=? WHERE id=? AND state='reserved'`), r.ClaimToken, stamp(r.ClaimUntil), stamp(now), saved.ID)
		if err != nil {
			return saved, false, err
		}
		saved.ClaimToken, saved.ClaimUntil, saved.UpdatedAt = r.ClaimToken, r.ClaimUntil, now
		claimed = true
	}
	return saved, claimed, tx.Commit()
}

// BindMutationAcceptance must run in the operation creation transaction. An
// expired or replaced reservation cannot schedule work after another caller won.
func (s *SQLStore) BindMutationAcceptance(ctx context.Context, tx *sql.Tx, kind, id string) error {
	claim, ok := core.MutationAcceptanceFromContext(ctx)
	if !ok {
		return nil
	}
	if claim.OperationKind != kind || claim.OperationID != id {
		return ErrMutationClaimLost
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE public_mutation_receipts SET state='accepted',claim_token='',message='Operation accepted.',updated_at=? WHERE id=? AND claim_token=? AND state='reserved' AND operation_kind=? AND operation_id=? AND retry_until>?`), stamp(now), claim.ReceiptID, claim.ClaimToken, kind, id, stamp(now))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrMutationClaimLost
	}
	return err
}
func (s *SQLStore) FailMutationReceipt(ctx context.Context, claim core.MutationAcceptance, status int, message string, now time.Time) error {
	if status < 400 || status > 599 || len(message) > 1024 {
		return errors.New("invalid mutation rejection")
	}
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE public_mutation_receipts SET state='failed',failure_status=?,message=?,claim_token='',updated_at=? WHERE id=? AND state='reserved' AND claim_token=?`), status, message, stamp(now), claim.ReceiptID, claim.ClaimToken)
	return err
}

// MutationOperationState reads the original workflow; receipts do not introduce
// a second executor or queue. Messages and provider payloads stay in that API.
func (s *SQLStore) MutationOperationState(ctx context.Context, kind, id string) (string, bool, error) {
	var state string
	var cancelled bool
	var err error
	switch kind {
	case "application":
		err = s.db.QueryRowContext(ctx, s.q(`SELECT 'succeeded' FROM apps WHERE id=?`), id).Scan(&state)
	case "workload_backup":
		err = s.db.QueryRowContext(ctx, s.q(`SELECT state FROM workload_backup_operations WHERE id=?`), id).Scan(&state)
	case "service_provision":
		err = s.db.QueryRowContext(ctx, s.q(`SELECT state FROM service_provision_runs WHERE id=?`), id).Scan(&state)
	case "service_resource":
		err = s.db.QueryRowContext(ctx, s.q(`SELECT state FROM service_resources WHERE operation_id=?`), id).Scan(&state)
		if state == "deleted" {
			state = "succeeded"
		}
	case "temporary_environment":
		err = s.db.QueryRowContext(ctx, s.q(`SELECT CASE WHEN state='closed' THEN 'succeeded' WHEN state='cleanup_blocked' THEN 'unknown' ELSE state END FROM temporary_environments WHERE cleanup_operation_id=?`), id).Scan(&state)
	case "deployment":
		err = s.db.QueryRowContext(ctx, s.q(`SELECT state FROM deployments WHERE id=?`), id).Scan(&state)
	case "infrastructure_operation":
		err = s.db.QueryRowContext(ctx, s.q(`SELECT state,cancel_requested FROM infrastructure_operations WHERE id=?`), id).Scan(&state, &cancelled)
	default:
		return "", false, ErrNotFound
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return state, cancelled, err
}

// UpdateMutationOutcome keeps a compact terminal result even if operation
// history is later pruned. Call it in the original operation's update transaction.
func (s *SQLStore) UpdateMutationOutcome(ctx context.Context, tx *sql.Tx, kind, id, state string) error {
	message := "Operation accepted."
	switch state {
	case "succeeded":
		message = "Operation completed."
	case "failed":
		message = "Operation failed; inspect its sanitized diagnostics."
	case "cancelled", "canceled":
		state = "cancelled"
		message = "Execution stopped after cancellation; external changes may remain."
	case "unknown", "unresolved":
		state = "unresolved"
		message = "The external outcome is uncertain; inspect or reconcile the original operation."
	default:
		state = "accepted"
	}
	_, err := tx.ExecContext(ctx, s.q(`UPDATE public_mutation_receipts SET state=?,message=?,updated_at=? WHERE operation_kind=? AND operation_id=? AND failure_status=0 AND state<>'reserved'`), state, message, stamp(time.Now().UTC()), kind, id)
	return err
}
