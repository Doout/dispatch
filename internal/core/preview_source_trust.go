package core

import "time"

// PreviewSourceOrigin records provider-verified repository identities, never
// credential values or assertions supplied by a comment author.
type PreviewSourceOrigin struct {
	Alias            string `json:"alias"`
	Repository       string `json:"repository"`
	RepositoryID     int64  `json:"repositoryId"`
	HeadRepository   string `json:"headRepository"`
	HeadRepositoryID int64  `json:"headRepositoryId"`
	Fork             bool   `json:"fork"`
	PullRequest      int    `json:"pullRequest"`
	CommitSHA        string `json:"commitSha"`
	GitHubAppID      string `json:"githubAppId"`
}

type PreviewSourceTrustDecision struct {
	Policy          string                `json:"policy"`
	Digest          string                `json:"digest"`
	Allowed         bool                  `json:"allowed"`
	Reason          string                `json:"reason"`
	Sources         []PreviewSourceOrigin `json:"sources"`
	CredentialScope []string              `json:"credentialScope"`
	Environments    []string              `json:"environments"`
	ApprovalID      string                `json:"approvalId,omitempty"`
	CheckedAt       time.Time             `json:"checkedAt"`
}

type PreviewSourceTrustApproval struct {
	ID         string     `json:"id"`
	ResourceID string     `json:"resourceId"`
	Digest     string     `json:"digest"`
	ActorID    string     `json:"actorId"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}
