package core

import "time"

// WorkflowCheckReport is independent reporting progress for captured execution.
// Updating it never changes the workflow, deployment, stage or job verdict.
type WorkflowCheckReport struct {
	ID            string    `json:"id"`
	RevisionID    string    `json:"revisionId"`
	ResourceID    string    `json:"resourceId"`
	ProjectID     string    `json:"projectId"`
	GitHubAppID   string    `json:"githubAppId"`
	Repository    string    `json:"repository"`
	CommitSHA     string    `json:"commitSha"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	Stage         string    `json:"stage,omitempty"`
	Check         string    `json:"check,omitempty"`
	PreviewURL    string    `json:"previewUrl,omitempty"`
	ExternalID    string    `json:"externalId"`
	CheckID       int64     `json:"checkId,omitempty"`
	HTMLURL       string    `json:"htmlUrl,omitempty"`
	Status        string    `json:"status,omitempty"`
	Conclusion    string    `json:"conclusion,omitempty"`
	State         string    `json:"state"`
	Error         string    `json:"error,omitempty"`
	Attempts      int       `json:"attempts"`
	Complete      bool      `json:"complete"`
	UpdatedAt     time.Time `json:"updatedAt"`
	NextAttemptAt time.Time `json:"nextAttemptAt,omitempty"`
	AppID         int64     `json:"-"`
	APIURL        string    `json:"-"`
	CreateState   string    `json:"-"`
	Digest        string    `json:"-"`
	LeaseToken    string    `json:"-"`
	LeaseUntil    time.Time `json:"-"`
}
