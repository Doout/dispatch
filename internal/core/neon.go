package core

import "time"

// NeonProvider binds a credential and API destination to one Dispatch project.
// Only owners configure it; project operators may use its saved templates.
type NeonProvider struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"projectId"`
	Name           string    `json:"name"`
	Endpoint       string    `json:"endpoint"`
	NeonProjectID  string    `json:"neonProjectId"`
	ParentBranchID string    `json:"parentBranchId"`
	CredentialRef  string    `json:"credentialRef"`
	CreatedAt      time.Time `json:"createdAt"`
}
