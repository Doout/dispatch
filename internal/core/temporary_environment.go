package core

import "time"

// TemporaryEnvironment owns one generated application and its execution history.
// The assigned runtime and any retained storage remain independently owned.
type TemporaryEnvironment struct {
	ID                 string              `json:"id"`
	ProjectID          string              `json:"projectId"`
	Name               string              `json:"name"`
	TemplateID         string              `json:"templateId"`
	TemplateDigest     string              `json:"templateDigest"`
	SourceSHA          string              `json:"sourceSha"`
	AppID              string              `json:"appId"`
	DeploymentID       string              `json:"deploymentId"`
	TargetNodeID       string              `json:"targetNodeId"`
	TargetGeneration   int64               `json:"targetGeneration"`
	ServerID           string              `json:"serverId"`
	Actor              Identity            `json:"actor"`
	State              string              `json:"state"`
	Message            string              `json:"message,omitempty"`
	Revision           int64               `json:"revision"`
	ExpiresAt          time.Time           `json:"expiresAt"`
	CreatedAt          time.Time           `json:"createdAt"`
	UpdatedAt          time.Time           `json:"updatedAt"`
	LeaseToken         string              `json:"-"`
	LeaseUntil         time.Time           `json:"-"`
	CleanupOperationID string              `json:"cleanupOperationId"`
	CleanupJobID       string              `json:"cleanupJobId"`
	Resources          []TemporaryResource `json:"resources"`
}
type TemporaryResource struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Ownership string `json:"ownership"`
}
type TemporaryEnvironmentInput struct {
	ProjectID       string `json:"projectId"`
	TemplateID      string `json:"templateId"`
	ServerID        string `json:"serverId"`
	Name            string `json:"name"`
	SourceSHA       string `json:"sourceSha"`
	LifetimeSeconds int64  `json:"lifetimeSeconds"`
}
type TemporaryEnvironmentReview struct {
	TargetNodeID     string                    `json:"targetNodeId"`
	TargetGeneration int64                     `json:"targetGeneration"`
	ID               string                    `json:"id"`
	EnvironmentID    string                    `json:"environmentId"`
	Input            TemporaryEnvironmentInput `json:"input"`
	TemplateDigest   string                    `json:"templateDigest"`
	Clone            App                       `json:"clone"`
	Omissions        []string                  `json:"omissions"`
	Digest           string                    `json:"digest"`
	State            string                    `json:"state"`
	ExpiresAt        time.Time                 `json:"expiresAt"`
	CreatedAt        time.Time                 `json:"createdAt"`
}
