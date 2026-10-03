package core

import "time"

// WorkloadBackupPolicy authorizes captures and retention for one frozen owned source.
type WorkloadBackupPolicy struct {
	OffsiteStoreID       string     `json:"offsiteStoreId,omitempty"`
	ID                   string     `json:"id"`
	ProjectID            string     `json:"projectId"`
	SourceRunID          string     `json:"sourceRunId"`
	SourceResourceID     string     `json:"sourceResourceId"`
	ServerID             string     `json:"serverId"`
	NodeID               string     `json:"nodeId,omitempty"`
	NodeGeneration       int64      `json:"nodeGeneration,omitempty"`
	Name                 string     `json:"name"`
	Enabled              bool       `json:"enabled"`
	Revision             int64      `json:"revision"`
	IntervalHours        int        `json:"intervalHours"`
	KeepLast             int        `json:"keepLast"`
	NextCaptureAt        time.Time  `json:"nextCaptureAt"`
	LastScheduledAt      *time.Time `json:"lastScheduledAt,omitempty"`
	LastAttemptAt        *time.Time `json:"lastAttemptAt,omitempty"`
	LastSuccessAt        *time.Time `json:"lastSuccessAt,omitempty"`
	LastBackupID         string     `json:"lastBackupId,omitempty"`
	LastVerifiedBackupID string     `json:"lastVerifiedBackupId,omitempty"`
	MissedCaptures       int64      `json:"missedCaptures"`
	State                string     `json:"state"`
	Message              string     `json:"message,omitempty"`
	Actor                Identity   `json:"actor"`
	EncryptedInput       string     `json:"-"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}

// DueCapture coalesces overdue slots while preserving the originally accepted cadence.
func (p WorkloadBackupPolicy) DueCapture(now time.Time) (slot, next time.Time, missed int64) {
	interval := time.Duration(p.IntervalHours) * time.Hour
	if interval <= 0 || p.NextCaptureAt.After(now) {
		return time.Time{}, p.NextCaptureAt, 0
	}
	missed = int64(now.Sub(p.NextCaptureAt) / interval)
	slot = p.NextCaptureAt.Add(time.Duration(missed) * interval)
	return slot, slot.Add(interval), missed
}
