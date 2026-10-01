package core

import "time"

// RuntimeJob payloads and results are encrypted; only scoped operation metadata
// is returned by the controller API. The lease is never a user API credential.
type RuntimeJob struct {
	ID               string    `json:"id"`
	ServerID         string    `json:"serverId"`
	NodeGeneration   int64     `json:"-"`
	Attempt          int       `json:"-"`
	NodeID           string    `json:"nodeId"`
	ProjectID        string    `json:"projectId"`
	AppID            string    `json:"appId"`
	ServiceRunID     string    `json:"serviceRunId,omitempty"`
	DeploymentID     string    `json:"deploymentId,omitempty"`
	Operation        string    `json:"operation"`
	State            string    `json:"state"`
	RequestDigest    string    `json:"-"`
	EncryptedRequest string    `json:"-"`
	EncryptedResult  string    `json:"-"`
	LeaseToken       string    `json:"-"`
	LeaseUntil       time.Time `json:"-"`
	CancelRequested  bool      `json:"cancelRequested"`
	Phase            string    `json:"phase,omitempty"`
	Message          string    `json:"message,omitempty"`
	ExpiresAt        time.Time `json:"expiresAt"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

func (j RuntimeJob) Terminal() bool {
	switch j.State {
	case "succeeded", "failed", "cancelled", "unknown", "acknowledged":
		return true
	}
	return false
}
