package core

import "time"

// WorkloadBackupPolicy authorizes captures and retention for one frozen owned source.
type WorkloadBackupPolicy struct {
	RetireLocalAfterOffsiteVerification bool       `json:"retireLocalAfterOffsiteVerification,omitempty"`
	RetentionBlockedReason              string     `json:"retentionBlockedReason,omitempty"`
	OffsiteStoreID                      string     `json:"offsiteStoreId,omitempty"`
	OffsiteStoreDigest                  string     `json:"offsiteStoreDigest,omitempty"`
	OffsiteStaleAfterHours              int        `json:"offsiteStaleAfterHours,omitempty"`
	NotificationAppID                   string     `json:"notificationAppId,omitempty"`
	OffsiteState                        string     `json:"offsiteState,omitempty"`
	OffsiteFreshness                    string     `json:"offsiteFreshness,omitempty"`
	OffsiteMessage                      string     `json:"offsiteMessage,omitempty"`
	LastOffsiteBackupID                 string     `json:"lastOffsiteBackupId,omitempty"`
	LastOffsiteOperationID              string     `json:"lastOffsiteOperationId,omitempty"`
	LastOffsiteRecoveryPointAt          *time.Time `json:"lastOffsiteRecoveryPointAt,omitempty"`
	LastOffsiteVerifiedAt               *time.Time `json:"lastOffsiteVerifiedAt,omitempty"`
	MissedExports                       int64      `json:"missedExports"`
	OffsiteNotificationState            string     `json:"offsiteNotificationState,omitempty"`
	OffsiteNotificationSequence         int64      `json:"offsiteNotificationSequence,omitempty"`
	ID                                  string     `json:"id"`
	ProjectID                           string     `json:"projectId"`
	SourceRunID                         string     `json:"sourceRunId"`
	SourceResourceID                    string     `json:"sourceResourceId"`
	ServerID                            string     `json:"serverId"`
	NodeID                              string     `json:"nodeId,omitempty"`
	NodeGeneration                      int64      `json:"nodeGeneration,omitempty"`
	Name                                string     `json:"name"`
	Enabled                             bool       `json:"enabled"`
	Revision                            int64      `json:"revision"`
	IntervalHours                       int        `json:"intervalHours"`
	KeepLast                            int        `json:"keepLast"`
	NextCaptureAt                       time.Time  `json:"nextCaptureAt"`
	LastScheduledAt                     *time.Time `json:"lastScheduledAt,omitempty"`
	LastAttemptAt                       *time.Time `json:"lastAttemptAt,omitempty"`
	LastSuccessAt                       *time.Time `json:"lastSuccessAt,omitempty"`
	LastBackupID                        string     `json:"lastBackupId,omitempty"`
	LastVerifiedBackupID                string     `json:"lastVerifiedBackupId,omitempty"`
	MissedCaptures                      int64      `json:"missedCaptures"`
	State                               string     `json:"state"`
	Message                             string     `json:"message,omitempty"`
	Actor                               Identity   `json:"actor"`
	EncryptedInput                      string     `json:"-"`
	CreatedAt                           time.Time  `json:"createdAt"`
	UpdatedAt                           time.Time  `json:"updatedAt"`
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

// OffsiteFreshnessAt measures the age of the captured data, not the upload time.
func (p WorkloadBackupPolicy) OffsiteFreshnessAt(now time.Time) string {
	if p.OffsiteStoreID == "" {
		return "disabled"
	}
	threshold := time.Duration(p.OffsiteStaleAfterHours) * time.Hour
	if p.LastOffsiteRecoveryPointAt == nil {
		if now.Sub(p.CreatedAt) > threshold {
			return "stale"
		}
		return "not_protected"
	}
	if now.Sub(*p.LastOffsiteRecoveryPointAt) > threshold {
		return "stale"
	}
	return "fresh"
}

// RetentionBlocker explains whether older local bytes can be removed automatically.
func (p WorkloadBackupPolicy) RetentionBlocker() string {
	if p.OffsiteStoreID == "" {
		return ""
	}
	if !p.RetireLocalAfterOffsiteVerification {
		return "Automatic local retirement is disabled. Local archives can exceed keepLast; explicitly approve retireLocalAfterOffsiteVerification or retire reviewed copies manually."
	}
	if !p.Enabled {
		return "Automatic local retirement is paused."
	}
	if p.State == "blocked" || p.OffsiteState == "blocked" || p.OffsiteState == "export_failed" || p.OffsiteState == "verification_failed" {
		return "Resolve the policy's capture or offsite blocker before automatic local retirement."
	}
	if p.LastBackupID == "" || p.LastOffsiteBackupID != p.LastBackupID {
		return "The newest capture must pass offsite restore verification before older local archives retire."
	}
	return ""
}
