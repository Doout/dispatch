package core

import "time"

const (
	ConfigSyncWebhookPoll = "webhook_poll"
	ConfigSyncWebhook     = "webhook"
	ConfigSyncPoll        = "poll"
)

type ConfigSource struct {
	ID                  string     `json:"id"`
	ProjectID           string     `json:"projectId"`
	GitHubAppID         string     `json:"githubAppId,omitempty"`
	CredentialSecretID  string     `json:"credentialSecretId,omitempty"`
	Name                string     `json:"name"`
	Repository          string     `json:"repository"`
	Branch              string     `json:"branch"`
	Path                string     `json:"path"`
	SyncMode            string     `json:"syncMode"`
	PollIntervalSeconds int        `json:"pollIntervalSeconds"`
	Active              bool       `json:"active"`
	State               string     `json:"state"`
	LastSeenSHA         string     `json:"lastSeenSha,omitempty"`
	LastSyncedAt        *time.Time `json:"lastSyncedAt,omitempty"`
	LastPolledAt        *time.Time `json:"lastPolledAt,omitempty"`
	LastError           string     `json:"lastError,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

type WorkflowResource struct {
	ID             string    `json:"id"`
	ConfigSourceID string    `json:"configSourceId"`
	APIVersion     string    `json:"apiVersion"`
	Kind           string    `json:"kind"`
	Name           string    `json:"name"`
	Path           string    `json:"path"`
	Document       string    `json:"document"`
	SpecDigest     string    `json:"specDigest"`
	ConfigSHA      string    `json:"configSha"`
	Active         bool      `json:"active"`
	State          string    `json:"state"`
	LastError      string    `json:"lastError,omitempty"`
	SourceCount    int       `json:"sourceCount"`
	JobCount       int       `json:"jobCount"`
	StageNames     []string  `json:"stageNames,omitempty"`
	TargetRefs     []string  `json:"targetRefs,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type WorkflowSourceRevision struct {
	Alias      string `json:"alias"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	CommitSHA  string `json:"commitSha"`
	Path       string `json:"path,omitempty"`
}

type WorkflowRevision struct {
	ID         string                            `json:"id"`
	ResourceID string                            `json:"resourceId"`
	ConfigSHA  string                            `json:"configSha"`
	SpecDigest string                            `json:"specDigest"`
	State      string                            `json:"state"`
	Trigger    string                            `json:"trigger"`
	Sources    map[string]WorkflowSourceRevision `json:"sources"`
	Outputs    map[string]map[string]string      `json:"outputs,omitempty"`
	Error      string                            `json:"error,omitempty"`
	CreatedAt  time.Time                         `json:"createdAt"`
	StartedAt  *time.Time                        `json:"startedAt,omitempty"`
	FinishedAt *time.Time                        `json:"finishedAt,omitempty"`
}

type WorkflowJobResult struct {
	ID           string                            `json:"id"`
	ResourceID   string                            `json:"resourceId"`
	RevisionID   string                            `json:"revisionId"`
	JobName      string                            `json:"jobName"`
	Fingerprint  string                            `json:"fingerprint"`
	ReusedFromID string                            `json:"reusedFromId,omitempty"`
	State        string                            `json:"state"`
	Sources      map[string]WorkflowSourceRevision `json:"sources"`
	Outputs      map[string]string                 `json:"outputs,omitempty"`
	Log          string                            `json:"log,omitempty"`
	Error        string                            `json:"error,omitempty"`
	CreatedAt    time.Time                         `json:"createdAt"`
	StartedAt    *time.Time                        `json:"startedAt,omitempty"`
	FinishedAt   *time.Time                        `json:"finishedAt,omitempty"`
}

type WorkflowStageRun struct {
	ID            string            `json:"id"`
	RevisionID    string            `json:"revisionId"`
	StageName     string            `json:"stageName"`
	TargetRef     string            `json:"targetRef"`
	State         string            `json:"state"`
	Approval      string            `json:"approval"`
	DeploymentIDs []string          `json:"deploymentIds,omitempty"`
	CheckRuns     map[string]string `json:"checkRuns,omitempty"`
	Error         string            `json:"error,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
	StartedAt     *time.Time        `json:"startedAt,omitempty"`
	FinishedAt    *time.Time        `json:"finishedAt,omitempty"`
}

type WorkflowEvent struct {
	ID             string     `json:"id"`
	ConfigSourceID string     `json:"configSourceId"`
	Provider       string     `json:"provider"`
	DeliveryID     string     `json:"deliveryId"`
	Kind           string     `json:"kind"`
	Repository     string     `json:"repository"`
	Branch         string     `json:"branch"`
	CommitSHA      string     `json:"commitSha"`
	State          string     `json:"state"`
	Error          string     `json:"error,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	ProcessedAt    *time.Time `json:"processedAt,omitempty"`
}
