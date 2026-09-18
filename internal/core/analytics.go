package core

import "time"

// AnalyticsEvent contains only completed-run facts, never source documents,
// command output, secret values, or deployment snapshots.
type AnalyticsEvent struct {
	ID                                     int64
	Kind, EntityID, ProjectID, Name, State string
	StartedAt, FinishedAt                  time.Time
	Reused                                 bool
}
