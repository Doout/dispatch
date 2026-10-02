package core

import "time"

// Cleanup evidence is retained after a preview and its runtime have gone away.
type WorkflowPreviewCleanup struct {
	Services   []WorkflowPreviewCleanupService `json:"services,omitempty"`
	ID         string                          `json:"id"`
	ResourceID string                          `json:"resourceId"`
	Reason     string                          `json:"reason"`
	FinalState string                          `json:"finalState"`
	State      string                          `json:"state"`
	Error      string                          `json:"error,omitempty"`
	Attempts   int                             `json:"attempts"`
	CreatedAt  time.Time                       `json:"createdAt"`
	UpdatedAt  time.Time                       `json:"updatedAt"`
	LeaseToken string                          `json:"-"`
	LeaseUntil time.Time                       `json:"-"`
	Apps       []WorkflowPreviewCleanupApp     `json:"apps"`
}
type WorkflowPreviewCleanupApp struct {
	PreviousJobIDs []string `json:"previousJobIds,omitempty"`
	NodeID         string   `json:"nodeId,omitempty"`
	NodeGeneration int64    `json:"nodeGeneration,omitempty"`
	AppID          string   `json:"appId"`
	ServerID       string   `json:"serverId"`
	SpecDigest     string   `json:"specDigest"`
	JobID          string   `json:"jobId"`
	State          string   `json:"state"`
	Error          string   `json:"error,omitempty"`
}

type WorkflowPreviewCleanupService struct {
	RunID         string `json:"runId"`
	Alias         string `json:"alias"`
	Policy        string `json:"policy"`
	ResourceID    string `json:"resourceId,omitempty"`
	OperationID   string `json:"operationId"`
	State         string `json:"state"`
	ActionStarted bool   `json:"actionStarted"`
	Error         string `json:"error,omitempty"`
}
