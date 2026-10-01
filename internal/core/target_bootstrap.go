package core

import "time"

// TargetBootstrapPlan contains only reviewable, non-secret installation inputs.
// Credentials and rendered user-data are stored separately with authenticated encryption.
type TargetBootstrapPlan struct {
	TargetName      string   `json:"targetName"`
	ControllerURL   string   `json:"controllerUrl"`
	ArtifactURL     string   `json:"artifactUrl"`
	ArtifactSHA256  string   `json:"artifactSha256"`
	Platform        string   `json:"platform"`
	ImageFamily     string   `json:"imageFamily"`
	InstallRuntime  bool     `json:"installRuntime"`
	ReplaceIdentity bool     `json:"replaceIdentity"`
	Method          string   `json:"method"`
	SSHHost         string   `json:"sshHost,omitempty"`
	SSHPort         int      `json:"sshPort,omitempty"`
	SSHUser         string   `json:"sshUser,omitempty"`
	SSHHostKey      string   `json:"sshHostKey,omitempty"`
	SSHFingerprint  string   `json:"sshFingerprint,omitempty"`
	SSHVerified     bool     `json:"sshVerified,omitempty"`
	Actions         []string `json:"actions"`
}

type TargetBootstrap struct {
	ID                 string              `json:"id"`
	ReviewID           string              `json:"reviewId,omitempty"`
	ServerID           string              `json:"serverId"`
	NodeID             string              `json:"nodeId"`
	ProviderID         string              `json:"providerId,omitempty"`
	ProjectID          string              `json:"projectId,omitempty"`
	Plan               TargetBootstrapPlan `json:"plan"`
	Digest             string              `json:"digest"`
	State              string              `json:"state"`
	InstallationState  string              `json:"installationState"`
	EnrollmentState    string              `json:"enrollmentState"`
	RuntimeState       string              `json:"runtimeState"`
	Message            string              `json:"message"`
	ActorID            string              `json:"actorId"`
	Generation         int64               `json:"generation,omitempty"`
	ExpectedGeneration int64               `json:"expectedGeneration"`
	Revision           int64               `json:"revision"`
	CreatedAt          time.Time           `json:"createdAt"`
	UpdatedAt          time.Time           `json:"updatedAt"`
	ReviewExpiresAt    time.Time           `json:"reviewExpiresAt"`
	ClaimExpiresAt     time.Time           `json:"claimExpiresAt"`
	AcceptedAt         *time.Time          `json:"acceptedAt,omitempty"`
	LeaseUntil         *time.Time          `json:"leaseUntil,omitempty"`
	ClaimHash          string              `json:"-"`
	EncryptedInput     string              `json:"-"`
}
