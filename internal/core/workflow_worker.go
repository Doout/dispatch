package core

import "time"

// WorkflowWorkerJob is the durable lease record. Payloads, progress and results
// are encrypted in the tenant's own operational store.
type WorkflowWorkerJob struct {
	ID, NodeID, ProjectID, ResourceID, RevisionID, Kind, Mode string
	Generation                                                int64
	Attempt                                                   int
	State, Digest, Request, Progress, Result, LeaseToken      string
	LeaseUntil, ExpiresAt, CreatedAt, UpdatedAt               time.Time
	CancelRequested                                           bool
}
