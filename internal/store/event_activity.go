package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/doout/dispatch/internal/sourceerror"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

type eventActivityWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *SQLStore) SaveEventActivity(ctx context.Context, key string, item core.EventActivity) error {
	return s.saveEventActivity(ctx, s.db, key, item)
}

func (s *SQLStore) saveEventActivity(ctx context.Context, writer eventActivityWriter, key string, item core.EventActivity) error {
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = writer.ExecContext(ctx, s.q(`INSERT INTO event_activity(id,project_id,dedup_key,rule_id,transport,is_check,state,created_at,payload,resource_id,revision_ids,preview_url) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(project_id,dedup_key) DO UPDATE SET payload=excluded.payload,state=excluded.state,
 created_at=CASE WHEN event_activity.is_check=TRUE THEN excluded.created_at ELSE event_activity.created_at END,
 resource_id=CASE WHEN excluded.resource_id<>'' THEN excluded.resource_id ELSE event_activity.resource_id END,
 revision_ids=CASE WHEN excluded.revision_ids<>'[]' THEN excluded.revision_ids ELSE event_activity.revision_ids END,
 preview_url=CASE WHEN excluded.preview_url<>'' THEN excluded.preview_url ELSE event_activity.preview_url END
 WHERE event_activity.is_check=TRUE OR event_activity.state IN ('queued','running','failed','deferred','retry')
 OR (event_activity.revision_ids='[]' AND excluded.revision_ids<>'[]')`),
		item.ID, item.ProjectID, key, item.RuleID, item.Transport, item.Check, item.State, stamp(item.CreatedAt), string(payload), item.ResourceID, jsonText(nonNilRevisionIDs(item.RevisionIDs)), item.PreviewURL)
	return err
}

func (s *SQLStore) SearchEventActivity(ctx context.Context, search core.EventActivitySearch) ([]core.EventActivity, error) {
	items := []core.EventActivity{}
	if len(search.ProjectIDs) == 0 {
		return items, nil
	}
	args := []any{}
	marks := make([]string, len(search.ProjectIDs))
	for i, id := range search.ProjectIDs {
		marks[i] = "?"
		args = append(args, id)
	}
	query := "SELECT id,created_at,transport,payload,resource_id,revision_ids,preview_url FROM event_activity WHERE project_id IN (" + strings.Join(marks, ",") + ")"
	if search.ChecksOnly {
		query += " AND is_check=TRUE"
	} else if !search.IncludeChecks {
		query += " AND is_check=FALSE" + s.eventActivityHistoryFilter()
	}
	if search.Transport != "" {
		query += " AND transport=?"
		args = append(args, search.Transport)
	}
	// IDs are sortable ULIDs. Poll checks retain their ID and are excluded from paged event history.
	if search.Before != "" {
		query += " AND id<?"
		args = append(args, search.Before)
	}
	if search.ChecksOnly || search.IncludeChecks {
		query += " ORDER BY created_at DESC,id DESC"
	} else {
		query += " ORDER BY id DESC"
	}
	limit := search.Limit
	if limit <= 0 {
		limit = 51
	}
	if !search.ChecksOnly {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, created, transport, payload, resourceID, revisionIDs, previewURL string
		if err := rows.Scan(&id, &created, &transport, &payload, &resourceID, &revisionIDs, &previewURL); err != nil {
			return nil, err
		}
		var item core.EventActivity
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return nil, err
		}
		item.ID, item.CreatedAt, item.Transport = id, parseTime(created), transport
		if item.Transport == "poll" && item.State == "failed" && item.Message != "" {
			item.Message = sourceerror.Describe(errors.New(item.Message)).Message
		}
		if resourceID != "" {
			item.ResourceID = resourceID
		}
		if revisionIDs != "[]" {
			if err := json.Unmarshal([]byte(revisionIDs), &item.RevisionIDs); err != nil {
				return nil, err
			}
		}
		if previewURL != "" {
			item.PreviewURL = previewURL
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) CountEventActivity(ctx context.Context, search core.EventActivitySearch) (int, error) {
	if len(search.ProjectIDs) == 0 {
		return 0, nil
	}
	marks := make([]string, len(search.ProjectIDs))
	args := []any{}
	for i, id := range search.ProjectIDs {
		marks[i] = "?"
		args = append(args, id)
	}
	query := "SELECT COUNT(*) FROM event_activity WHERE is_check=FALSE AND project_id IN (" + strings.Join(marks, ",") + ")" + s.eventActivityHistoryFilter()
	if search.Transport != "" {
		query += " AND transport=?"
		args = append(args, search.Transport)
	}
	var count int
	err := s.db.QueryRowContext(ctx, s.q(query), args...).Scan(&count)
	return count, err
}

// Older controllers also recorded scans that did not start a run. Filter before
// pagination so these records do not consume pages or inflate the activity count.
func (s *SQLStore) eventActivityHistoryFilter() string {
	kind := "json_extract(payload,'$.kind')"
	if s.postgres {
		kind = "payload::json->>'kind'"
	}
	return " AND (transport<>'poll' OR state='failed' OR revision_ids<>'[]' OR COALESCE(" + kind + ",'') NOT IN ('branch_scan','repository_check','preview_check'))"
}

func nonNilRevisionIDs(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

// SavePollCheck keeps one current check per repository and journals failure and recovery transitions.
func (s *SQLStore) SavePollCheck(ctx context.Context, key string, item core.EventActivity) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var payload string
	query := "SELECT payload FROM event_activity WHERE project_id=? AND dedup_key=?"
	if s.postgres {
		query += " FOR UPDATE"
	}
	err = tx.QueryRowContext(ctx, s.q(query), item.ProjectID, key).Scan(&payload)
	var previous core.EventActivity
	if err == nil {
		if err = json.Unmarshal([]byte(payload), &previous); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if previous.State == "failed" {
		previous.Message = sourceerror.Describe(errors.New(previous.Message)).Message
	}
	if item.State == "failed" {
		item.Message = sourceerror.Describe(errors.New(item.Message)).Message
		item.Attempts = 1
		if previous.State == "failed" && previous.NextAttemptAt != nil {
			item.Attempts = previous.Attempts + 1
		}
		if item.NextAttemptAt != nil {
			base := item.NextAttemptAt.Sub(item.CreatedAt)
			if base < 30*time.Second {
				base = 30 * time.Second
			}
			ceiling := max(base, 15*time.Minute)
			delay := base
			// Bounded arithmetic also preserves intervals above the retry ceiling.
			for n := 1; n < item.Attempts && delay < ceiling; n++ {
				delay = min(ceiling, delay+min(delay, ceiling-delay))
			}
			next := item.CreatedAt.Add(delay)
			item.NextAttemptAt = &next
		}
	} else {
		item.Attempts = 0
		item.NextAttemptAt = nil
	}
	if item.State == "failed" && (previous.State != "failed" || previous.Message != item.Message) || item.State == "processed" && previous.State == "failed" {
		transition := item
		transition.Check = false
		transition.ID = ulid.Make().String()
		transition.Kind = "poll_failed"
		if item.State == "processed" {
			transition.Kind = "poll_recovered"
			transition.Message = "Polling resumed."
		}
		if err = s.saveEventActivity(ctx, tx, "poll-transition:"+transition.ID, transition); err != nil {
			return err
		}
	}
	if err = s.saveEventActivity(ctx, tx, key, item); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) GetPollCheck(ctx context.Context, projectID, key string) (core.EventActivity, error) {
	var raw string
	var item core.EventActivity
	if err := s.db.QueryRowContext(ctx, s.q("SELECT payload FROM event_activity WHERE project_id=? AND dedup_key=? AND is_check=TRUE"), projectID, key).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return item, ErrNotFound
		}
		return item, err
	}
	err := json.Unmarshal([]byte(raw), &item)
	return item, err
}
