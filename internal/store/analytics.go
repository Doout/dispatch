package store

import (
	"context"
	"strconv"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

func (s *SQLStore) ListAnalyticsEvents(ctx context.Context, limit int) ([]core.AnalyticsEvent, error) {
	if limit < 1 || limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, s.q("SELECT event_id,kind,entity_id,project_id,name,state,started_at,finished_at,reused FROM analytics_outbox ORDER BY event_id LIMIT ?"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []core.AnalyticsEvent{}
	for rows.Next() {
		var e core.AnalyticsEvent
		var started, finished string
		var reused int
		if err := rows.Scan(&e.ID, &e.Kind, &e.EntityID, &e.ProjectID, &e.Name, &e.State, &started, &finished, &reused); err != nil {
			return nil, err
		}
		e.StartedAt, e.FinishedAt, e.Reused = parseTime(started), parseTime(finished), reused != 0
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *SQLStore) AckAnalyticsEvents(ctx context.Context, events []core.AnalyticsEvent) error {
	if len(events) == 0 {
		return nil
	}
	// Acknowledge exact IDs. PostgreSQL transactions can commit sequence IDs out
	// of order, so deleting everything below a watermark could lose events.
	ids := make([]string, len(events))
	for i, e := range events {
		ids[i] = strconv.FormatInt(e.ID, 10)
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM analytics_outbox WHERE event_id IN ("+strings.Join(ids, ",")+")")
	return err
}
