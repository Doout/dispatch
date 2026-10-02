package core

import "time"

// EventActivity contains delivery metadata, never webhook bodies or command arguments.
type EventActivity struct {
	DeliveryID    string     `json:"deliveryId,omitempty"`
	Attempts      int        `json:"attempts,omitempty"`
	NextAttemptAt *time.Time `json:"nextAttemptAt,omitempty"`
	ID            string     `json:"id"`
	ProjectID     string     `json:"projectId"`
	RuleID        string     `json:"ruleId"`
	Name          string     `json:"name"`
	Transport     string     `json:"transport"`
	Kind          string     `json:"kind"`
	Repository    string     `json:"repository"`
	Branch        string     `json:"branch,omitempty"`
	CommitSHA     string     `json:"commitSha,omitempty"`
	PullRequest   int        `json:"pullRequest,omitempty"`
	Command       string     `json:"command,omitempty"`
	State         string     `json:"state"`
	Message       string     `json:"message,omitempty"`
	ResourceID    string     `json:"resourceId,omitempty"`
	RevisionIDs   []string   `json:"revisionIds,omitempty"`
	PreviewURL    string     `json:"previewUrl,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	Check         bool       `json:"check,omitempty"`
}

type EventActivitySearch struct {
	ProjectIDs    []string
	Transport     string
	IncludeChecks bool
	ChecksOnly    bool
	Before        string
	Limit         int
}
