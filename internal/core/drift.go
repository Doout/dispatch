package core

import "time"

// DriftBaseline is internal. Rendered resources may contain credentials.
type DriftBaseline struct {
	DeploymentID string
	AppID        string
	ServerID     string
	Namespace    string
	Release      string
	Ciphertext   string
}
type DriftDifference struct {
	Path     string `json:"path"`
	Expected any    `json:"expected"`
	Actual   any    `json:"actual"`
	Redacted bool   `json:"redacted,omitempty"`
}
type DriftResource struct {
	APIVersion  string            `json:"apiVersion"`
	Kind        string            `json:"kind"`
	Namespace   string            `json:"namespace,omitempty"`
	Name        string            `json:"name"`
	State       string            `json:"state"`
	Health      string            `json:"health"`
	Truncated   bool              `json:"truncated,omitempty"`
	Differences []DriftDifference `json:"differences"`
}
type DriftCheck struct {
	DeploymentID          string          `json:"deploymentId,omitempty"`
	State                 string          `json:"state"`
	Health                string          `json:"health"`
	Message               string          `json:"message"`
	CheckedAt             *time.Time      `json:"checkedAt,omitempty"`
	LastSuccessfulCheckAt *time.Time      `json:"lastSuccessfulCheckAt,omitempty"`
	Location              string          `json:"location"`
	Resources             []DriftResource `json:"resources"`
}
type DriftAction struct {
	ID           string    `json:"id"`
	AppID        string    `json:"appId"`
	DeploymentID string    `json:"deploymentId"`
	Actor        string    `json:"actor"`
	State        string    `json:"state"`
	Message      string    `json:"message"`
	CreatedAt    time.Time `json:"createdAt"`
}
