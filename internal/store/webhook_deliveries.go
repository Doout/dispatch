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

var ErrWebhookDeliveryConflict = errors.New("delivery identifier was already used for a different payload")

type WebhookDeliveryStore interface {
	AcceptWebhookDelivery(context.Context, core.WebhookDelivery) (core.WebhookDelivery, bool, error)
	ClaimWebhookDelivery(context.Context, time.Time, time.Duration) (core.WebhookDelivery, error)
	FinishWebhookDelivery(context.Context, core.WebhookDelivery, time.Time) error
	ListWebhookDeliveries(context.Context, string, int) ([]core.WebhookDelivery, error)
	RetainWebhookDeliveries(context.Context, time.Time) error
}

const webhookSelect = `SELECT id,connection_id,delivery_id,body_digest,event_name,repository,installation_id,state,attempts,error,received_at,expires_at,next_attempt_at,completed_at,result,ciphertext,lease_token,lease_until,activities FROM webhook_deliveries`

func scanWebhookDelivery(row scanner) (core.WebhookDelivery, error) {
	var r core.WebhookDelivery
	var received, expires, next, lease, activities string
	var completed sql.NullString
	err := row.Scan(&r.ID, &r.ConnectionID, &r.DeliveryID, &r.BodyDigest, &r.Event, &r.Repository, &r.InstallationID, &r.State, &r.Attempts, &r.Error, &received, &expires, &next, &completed, &r.Result, &r.Ciphertext, &r.LeaseToken, &lease, &activities)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.ReceivedAt, r.ExpiresAt, r.NextAttemptAt, r.LeaseUntil = parseTime(received), parseTime(expires), parseTime(next), parseTime(lease)
	if completed.Valid {
		t := parseTime(completed.String)
		r.CompletedAt = &t
	}
	err = json.Unmarshal([]byte(activities), &r.Activities)
	return r, err
}

// Both delivery IDs and signed-body digests remain as compact replay tombstones.
// GitHub's signature covers the body, not the caller supplied delivery header.
func (s *SQLStore) AcceptWebhookDelivery(ctx context.Context, r core.WebhookDelivery) (core.WebhookDelivery, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.q(`INSERT INTO webhook_deliveries(id,connection_id,delivery_id,body_digest,event_name,repository,installation_id,state,received_at,expires_at,next_attempt_at,ciphertext,activities) VALUES(?,?,?,?,?,?,?,'queued',?,?,?,?,?) ON CONFLICT DO NOTHING`), r.ID, r.ConnectionID, r.DeliveryID, r.BodyDigest, r.Event, r.Repository, r.InstallationID, stamp(r.ReceivedAt), stamp(r.ExpiresAt), stamp(r.ReceivedAt), r.Ciphertext, jsonText(r.Activities))
	if err != nil {
		return r, false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return r, false, err
	}
	if n == 0 {
		saved, err := scanWebhookDelivery(tx.QueryRowContext(ctx, s.q(webhookSelect+` WHERE connection_id=? AND delivery_id=?`), r.ConnectionID, r.DeliveryID))
		if errors.Is(err, ErrNotFound) {
			saved, err = scanWebhookDelivery(tx.QueryRowContext(ctx, s.q(webhookSelect+` WHERE connection_id=? AND body_digest=?`), r.ConnectionID, r.BodyDigest))
		}
		if err != nil {
			return r, false, err
		}
		if saved.BodyDigest != r.BodyDigest || saved.Event != r.Event {
			return saved, false, ErrWebhookDeliveryConflict
		}
		return saved, false, tx.Commit()
	}
	r.State, r.NextAttemptAt = "queued", r.ReceivedAt
	if err := s.webhookActivities(ctx, tx, r); err != nil {
		return r, false, err
	}
	return r, true, tx.Commit()
}

func (s *SQLStore) ClaimWebhookDelivery(ctx context.Context, now time.Time, lease time.Duration) (core.WebhookDelivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.WebhookDelivery{}, err
	}
	defer tx.Rollback()
	query := webhookSelect + ` WHERE expires_at>? AND ((state IN ('queued','retry') AND next_attempt_at<=?) OR (state='running' AND lease_until<=?)) ORDER BY received_at,id LIMIT 1`
	if s.postgres {
		query += ` FOR UPDATE SKIP LOCKED`
	}
	r, err := scanWebhookDelivery(tx.QueryRowContext(ctx, s.q(query), stamp(now), stamp(now), stamp(now)))
	if err != nil {
		return r, err
	}
	r.State, r.Attempts, r.LeaseToken, r.LeaseUntil = "running", r.Attempts+1, ulid.Make().String(), now.Add(lease)
	result, err := tx.ExecContext(ctx, s.q(`UPDATE webhook_deliveries SET state='running',attempts=?,lease_token=?,lease_until=? WHERE id=? AND ((state IN ('queued','retry') AND next_attempt_at<=?) OR (state='running' AND lease_until<=?))`), r.Attempts, r.LeaseToken, stamp(r.LeaseUntil), r.ID, stamp(now), stamp(now))
	if err := changed(result, err); err != nil {
		return r, err
	}
	if err := s.webhookActivities(ctx, tx, r); err != nil {
		return r, err
	}
	return r, tx.Commit()
}

// The attempt token fences a late worker after another process has reclaimed its lease.
func (s *SQLStore) FinishWebhookDelivery(ctx context.Context, r core.WebhookDelivery, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cipher := r.Ciphertext
	if r.State == "processed" || r.State == "rejected" || r.State == "expired" {
		cipher = ""
		r.CompletedAt = &now
	}
	result, err := tx.ExecContext(ctx, s.q(`UPDATE webhook_deliveries SET state=?,error=?,next_attempt_at=?,completed_at=?,result=?,ciphertext=?,lease_token='',lease_until='' WHERE id=? AND state='running' AND lease_token=? AND lease_until>?`), r.State, r.Error, stamp(r.NextAttemptAt), nullTime(r.CompletedAt), r.Result, cipher, r.ID, r.LeaseToken, stamp(now))
	if err := changed(result, err); err != nil {
		return err
	}
	if err := s.webhookActivities(ctx, tx, r); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) webhookActivities(ctx context.Context, tx eventActivityWriter, r core.WebhookDelivery) error {
	for _, a := range r.Activities {
		a.State, a.DeliveryID, a.Attempts, a.Message = r.State, r.ID, r.Attempts, r.Error
		if r.State == "retry" {
			a.NextAttemptAt = &r.NextAttemptAt
		}
		if err := s.saveEventActivity(ctx, tx, "webhook-receipt:"+r.ID+":"+a.RuleID, a); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLStore) ListWebhookDeliveries(ctx context.Context, before string, limit int) ([]core.WebhookDelivery, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query, args := webhookSelect, []any{}
	if before != "" {
		query += ` WHERE id<?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.WebhookDelivery{}
	for rows.Next() {
		r, err := scanWebhookDelivery(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// Erase private payloads after seven days and activity after thirty days. Never
// remove delivery, command or incoming-event identities, close watermarks or poll cursors.
func (s *SQLStore) RetainWebhookDeliveries(ctx context.Context, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, s.q(webhookSelect+` WHERE expires_at<=? AND state IN ('queued','retry','running') AND lease_until<=? LIMIT 100`), stamp(now), stamp(now))
	if err != nil {
		return err
	}
	items := []core.WebhookDelivery{}
	for rows.Next() {
		r, err := scanWebhookDelivery(rows)
		if err != nil {
			rows.Close()
			return err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, r := range items {
		r.State, r.Error = "expired", "Delivery retry window expired; inspect the linked workflow or cleanup status."
		result, err := tx.ExecContext(ctx, s.q(`UPDATE webhook_deliveries SET state='expired',ciphertext='',completed_at=?,error=?,lease_token='',lease_until='' WHERE id=? AND expires_at<=? AND state IN ('queued','retry','running') AND lease_until<=?`), stamp(now), r.Error, r.ID, stamp(now), stamp(now))
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			continue
		}
		if err := s.webhookActivities(ctx, tx, r); err != nil {
			return err
		}
	}
	cutoff := stamp(now.Add(-30 * 24 * time.Hour))
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM event_activity WHERE is_check=FALSE AND created_at<? AND state NOT IN ('queued','running','retry','deferred')`), cutoff); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE webhook_deliveries SET activities='[]',result='',error='' WHERE received_at<? AND ciphertext='' AND (activities<>'[]' OR result<>'' OR error<>'')`), cutoff); err != nil {
		return err
	}
	return tx.Commit()
}
