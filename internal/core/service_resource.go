package core

import "time"

// ServiceResource retains the ownership and encrypted recovery material for one
// provision run, independently of its connection registration or template.
type ServiceResource struct {
	PolicyActorID           string                 `json:"policyActorId,omitempty"`
	PolicyApprovedAt        *time.Time             `json:"policyApprovedAt,omitempty"`
	ReplacedByRunID         string                 `json:"replacedByRunId,omitempty"`
	ReplacesRevision        int64                  `json:"-"`
	ReplacesRunID           string                 `json:"replacesRunId,omitempty"`
	PreviewID               string                 `json:"previewId,omitempty"`
	PreviewAlias            string                 `json:"previewAlias,omitempty"`
	ProviderPhase           string                 `json:"providerPhase,omitempty"`
	ProviderCreateAttempted bool                   `json:"providerCreateAttempted,omitempty"`
	Name                    string                 `json:"name"`
	Dependencies            []string               `json:"dependencies,omitempty"`
	RunID                   string                 `json:"runId"`
	ProjectID               string                 `json:"projectId"`
	ServiceID               string                 `json:"serviceId"`
	Target                  ServiceProvisionTarget `json:"target"`
	State                   string                 `json:"state"`
	ResourceID              string                 `json:"resourceId,omitempty"`
	Policy                  string                 `json:"policy"`
	Revision                int64                  `json:"revision"`
	OperationID             string                 `json:"operationId"`
	Message                 string                 `json:"message,omitempty"`
	CreatedAt               time.Time              `json:"createdAt"`
	UpdatedAt               time.Time              `json:"updatedAt"`
	LeaseUntil              time.Time              `json:"recoveryAfter,omitempty"`
	LeaseToken              string                 `json:"-"`
	EncryptedRequest        string                 `json:"-"`
	EncryptedOutputs        string                 `json:"-"`
}

type ServiceResourceInspection struct {
	RunID           string `json:"runId"`
	ProjectID       string `json:"projectId"`
	ServerID        string `json:"serverId"`
	Provider        string `json:"provider"`
	ResourceID      string `json:"resourceId,omitempty"`
	State           string `json:"state"`
	StorageRetained bool   `json:"storageRetained"`
}
