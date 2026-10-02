package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/doout/dispatch/internal/core"
)

type workflowReceiptKey struct{}
type workflowPreviewTriggerKey struct{}

// WithWorkflowPreviewTrigger creates the owner of a generated preview in the
// same transaction as the resource, so interruption cannot leave an orphan.
func WithWorkflowPreviewTrigger(ctx context.Context, trigger core.WorkflowPreviewTrigger) context.Context {
	return context.WithValue(ctx, workflowPreviewTriggerKey{}, trigger)
}

type WorkflowReceipt struct{ Key, TriggerID, CommentID string }

// WithWorkflowReceipt binds scheduling to a durable event or logical comment.
// The receipt and revision are inserted in the same transaction.
func WithWorkflowReceipt(ctx context.Context, receipt WorkflowReceipt) context.Context {
	return context.WithValue(ctx, workflowReceiptKey{}, receipt)
}
func WorkflowReceiptFromContext(ctx context.Context) WorkflowReceipt {
	r, _ := ctx.Value(workflowReceiptKey{}).(WorkflowReceipt)
	return r
}
func (s *SQLStore) WorkflowReceiptRevision(ctx context.Context, resourceID, key string) (core.WorkflowRevision, error) {
	var id string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT revision_id FROM workflow_command_receipts WHERE resource_id=? AND command_key=?`), resourceID, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return core.WorkflowRevision{}, ErrNotFound
	}
	if err != nil {
		return core.WorkflowRevision{}, err
	}
	if id == "" {
		return core.WorkflowRevision{ResourceID: resourceID, State: "no_change"}, nil
	}
	r, err := s.GetWorkflowRevision(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return core.WorkflowRevision{ID: id, ResourceID: resourceID, State: "retained_receipt"}, nil
	}
	return r, err
}

func (s *SQLStore) RecordWorkflowNoChange(ctx context.Context, resourceID, key string) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO workflow_command_receipts(resource_id,command_key,revision_id) VALUES(?,?,'') ON CONFLICT DO NOTHING`), resourceID, key)
	return err
}

func (s *SQLStore) WorkflowEventsForDelivery(ctx context.Context, deliveryID string) ([]core.WorkflowEvent, error) {
	return s.workflowEvents(ctx, ` WHERE provider='github' AND delivery_id=? ORDER BY created_at,id`, deliveryID)
}
func (s *SQLStore) PendingWorkflowEvents(ctx context.Context) ([]core.WorkflowEvent, error) {
	return s.workflowEvents(ctx, ` WHERE provider='github' AND state IN ('queued','running') ORDER BY created_at,id LIMIT 100`)
}
func (s *SQLStore) workflowEvents(ctx context.Context, where string, args ...any) ([]core.WorkflowEvent, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id,config_source_id,provider,delivery_id,kind,repository,branch,commit_sha,state,error,created_at,processed_at,revision_ids FROM workflow_events`+where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.WorkflowEvent{}
	for rows.Next() {
		var r core.WorkflowEvent
		var created, ids string
		var processed sql.NullString
		if err := rows.Scan(&r.ID, &r.ConfigSourceID, &r.Provider, &r.DeliveryID, &r.Kind, &r.Repository, &r.Branch, &r.CommitSHA, &r.State, &r.Error, &created, &processed, &ids); err != nil {
			return nil, err
		}
		r.CreatedAt = parseTime(created)
		if processed.Valid {
			t := parseTime(processed.String)
			r.ProcessedAt = &t
		}
		if err := json.Unmarshal([]byte(ids), &r.RevisionIDs); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}
