package core

import "time"

// RuntimeRetentionItem identifies one exact runtime object. It contains no
// execution inputs, credentials, mount contents, or arbitrary deletion paths.
type RuntimeRetentionItem struct {
	Key           string    `json:"key"`
	Kind          string    `json:"kind"`
	ServerID      string    `json:"serverId"`
	AppID         string    `json:"appId"`
	DeploymentID  string    `json:"deploymentId,omitempty"`
	Name          string    `json:"name"`
	Identity      string    `json:"identity"`
	CreatedAt     time.Time `json:"createdAt"`
	Protected     []string  `json:"protected"`
	TargetBinding string    `json:"targetBinding,omitempty"`
	Containers    []string  `json:"containers,omitempty"`
}
type RuntimeRetentionOutcome struct {
	Key     string `json:"key"`
	State   string `json:"state"`
	Message string `json:"message"`
}
type RuntimeRetentionReview struct {
	SupersededBy string                    `json:"supersededBy,omitempty"`
	Attempt      string                    `json:"-"`
	ID           string                    `json:"id"`
	ProjectID    string                    `json:"projectId"`
	Digest       string                    `json:"digest"`
	Policy       RetentionPolicy           `json:"policy"`
	State        string                    `json:"state"`
	CreatedAt    time.Time                 `json:"createdAt"`
	ExpiresAt    time.Time                 `json:"expiresAt"`
	Items        []RuntimeRetentionItem    `json:"items"`
	Results      []RuntimeRetentionOutcome `json:"results"`
}
type RuntimeRetentionReference struct {
	DeploymentID, AppID string
	CreatedAt           time.Time
	Protected           []string
}

// RuntimeArtifactMetadata remains after encrypted inputs are retired, so image
// and revision policies can have different ages without losing the inventory.
type RuntimeArtifactMetadata struct {
	ProjectID   string            `json:"projectId,omitempty"`
	InputDigest string            `json:"inputDigest,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
	Images      map[string]string `json:"images"`
	Retired     bool              `json:"retired"`
}

func (p RetentionPolicy) RollbackRetentionCount() int {
	if p.KeepRollbackRevisions == 0 {
		return 5
	}
	return p.KeepRollbackRevisions
}
