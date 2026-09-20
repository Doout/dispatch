package core

import "time"

// ReleaseNote contains operator-authored release context, never execution inputs.
type ReleaseNote struct {
	DeploymentID string    `json:"deploymentId"`
	Notes        string    `json:"notes"`
	Links        []string  `json:"links"`
	Actor        string    `json:"actor"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type ReleaseAction struct {
	ID                 string    `json:"id"`
	ProjectID          string    `json:"projectId"`
	AppID              string    `json:"appId,omitempty"`
	DeploymentID       string    `json:"deploymentId,omitempty"`
	SourceDeploymentID string    `json:"sourceDeploymentId,omitempty"`
	Actor              string    `json:"actor"`
	Action             string    `json:"action"`
	Message            string    `json:"message"`
	CreatedAt          time.Time `json:"createdAt"`
}
