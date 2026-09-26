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
	LastEvaluation      *WorkflowEquivalence `json:"lastEvaluation,omitempty"`
	PreviewPullRequests []HelmPullRequest    `json:"previewPullRequests,omitempty"`
	ServiceIDs          []string             `json:"-"`
	ID                  string               `json:"id"`
	ConfigSourceID      string               `json:"configSourceId"`
	APIVersion          string               `json:"apiVersion"`
	Kind                string               `json:"kind"`
	Name                string               `json:"name"`
	Path                string               `json:"path"`
	Document            string               `json:"document"`
	SpecDigest          string               `json:"specDigest"`
	ConfigSHA           string               `json:"configSha"`
	Temporary           bool                 `json:"temporary"`
	Active              bool                 `json:"active"`
	State               string               `json:"state"`
	LastError           string               `json:"lastError,omitempty"`
	SourceCount         int                  `json:"sourceCount"`
	JobCount            int                  `json:"jobCount"`
	StageNames          []string             `json:"stageNames,omitempty"`
	TargetRefs          []string             `json:"targetRefs,omitempty"`
	CreatedAt           time.Time            `json:"createdAt"`
	UpdatedAt           time.Time            `json:"updatedAt"`
}

// WorkflowPreviewTrigger binds an inline workflow to one pull request command.
type WorkflowPreviewTrigger struct {
	TemplateSource     *WorkflowPreviewTemplateGitSource `json:"templateSource,omitempty"`
	LinkedPullRequests map[string]int                    `json:"linkedPullRequests,omitempty"`
	ID                 string                            `json:"id"`
	TemplateID         string                            `json:"templateId,omitempty"`
	ResourceID         string                            `json:"resourceId"`
	GitHubAppID        string                            `json:"githubAppId"`
	Repository         string                            `json:"repository"`
	PullRequestNumber  int                               `json:"pullRequestNumber"`
	Command            string                            `json:"command"`
	PreviewURL         string                            `json:"previewUrl,omitempty"`
	ReportCommentID    string                            `json:"reportCommentId,omitempty"`
	CreatedAt          time.Time                         `json:"createdAt"`
	ClosedAt           *time.Time                        `json:"closedAt,omitempty"`
}

// WorkflowPreviewTemplate creates one temporary Application per PR when its
// repository receives a trusted comment command.
type WorkflowPreviewTemplateGitSource struct {
	Repository string     `json:"repository"`
	Branch     string     `json:"branch"`
	Path       string     `json:"path"`
	CommitSHA  string     `json:"commitSha,omitempty"`
	SyncedAt   *time.Time `json:"syncedAt,omitempty"`
	LastError  string     `json:"lastError,omitempty"`
}

type WorkflowPreviewTemplate struct {
	WatchRepositories []string                          `json:"watchRepositories,omitempty"`
	GitSource         *WorkflowPreviewTemplateGitSource `json:"gitSource,omitempty"`
	ID                string                            `json:"id"`
	ConfigSourceID    string                            `json:"configSourceId"`
	GitHubAppID       string                            `json:"githubAppId"`
	Name              string                            `json:"name"`
	Repository        string                            `json:"repository"`
	Command           string                            `json:"command"`
	PreviewURL        string                            `json:"previewUrl"`
	Document          string                            `json:"document"`
	Active            bool                              `json:"active"`
	CreatedAt         time.Time                         `json:"createdAt"`
	UpdatedAt         time.Time                         `json:"updatedAt"`
}

type WorkflowSourceRevision struct {
	ContentHashes map[string]string `json:"contentHashes,omitempty"`
	Alias         string            `json:"alias"`
	Repository    string            `json:"repository"`
	Branch        string            `json:"branch"`
	CommitSHA     string            `json:"commitSha"`
	Path          string            `json:"path,omitempty"`
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
	DeploymentResults []WorkflowDeploymentResult `json:"deploymentResults,omitempty"`
	ID                string                     `json:"id"`
	RevisionID        string                     `json:"revisionId"`
	StageName         string                     `json:"stageName"`
	TargetRef         string                     `json:"targetRef"`
	State             string                     `json:"state"`
	Approval          string                     `json:"approval"`
	DeploymentIDs     []string                   `json:"deploymentIds,omitempty"`
	CheckRuns         map[string]string          `json:"checkRuns,omitempty"`
	Error             string                     `json:"error,omitempty"`
	CreatedAt         time.Time                  `json:"createdAt"`
	StartedAt         *time.Time                 `json:"startedAt,omitempty"`
	FinishedAt        *time.Time                 `json:"finishedAt,omitempty"`
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
