package core

import "time"

const (
	ConfigSyncWebhookPoll = "webhook_poll"
	ConfigSyncWebhook     = "webhook"
	ConfigSyncPoll        = "poll"
)

type ConfigSource struct {
	ID                  string            `json:"id"`
	ProjectID           string            `json:"projectId"`
	GitHubAppID         string            `json:"githubAppId,omitempty"`
	CredentialSecretID  string            `json:"credentialSecretId,omitempty"`
	Name                string            `json:"name"`
	Repository          string            `json:"repository"`
	RepositoryID        int64             `json:"repositoryId,omitempty"`
	RepositoryStatus    *RepositoryStatus `json:"repositoryStatus,omitempty"`
	Branch              string            `json:"branch"`
	Path                string            `json:"path"`
	SyncMode            string            `json:"syncMode"`
	PollIntervalSeconds int               `json:"pollIntervalSeconds"`
	Active              bool              `json:"active"`
	State               string            `json:"state"`
	LastSeenSHA         string            `json:"lastSeenSha,omitempty"`
	LastSyncedAt        *time.Time        `json:"lastSyncedAt,omitempty"`
	LastPolledAt        *time.Time        `json:"lastPolledAt,omitempty"`
	LastError           string            `json:"lastError,omitempty"`
	CreatedAt           time.Time         `json:"createdAt"`
	UpdatedAt           time.Time         `json:"updatedAt"`
}

type WorkflowResource struct {
	PreviewCleanups     []WorkflowPreviewCleanup `json:"previewCleanups,omitempty"`
	PreviewTTL          string                   `json:"previewTTL,omitempty"`
	PreviewExpiresAt    *time.Time               `json:"previewExpiresAt,omitempty"`
	LastEvaluation      *WorkflowEquivalence     `json:"lastEvaluation,omitempty"`
	PreviewPullRequests []HelmPullRequest        `json:"previewPullRequests,omitempty"`
	ServiceIDs          []string                 `json:"-"`
	ID                  string                   `json:"id"`
	ConfigSourceID      string                   `json:"configSourceId"`
	APIVersion          string                   `json:"apiVersion"`
	Kind                string                   `json:"kind"`
	Name                string                   `json:"name"`
	Path                string                   `json:"path"`
	Document            string                   `json:"document"`
	SpecDigest          string                   `json:"specDigest"`
	ConfigSHA           string                   `json:"configSha"`
	Temporary           bool                     `json:"temporary"`
	Active              bool                     `json:"active"`
	State               string                   `json:"state"`
	LastError           string                   `json:"lastError,omitempty"`
	SourceCount         int                      `json:"sourceCount"`
	JobCount            int                      `json:"jobCount"`
	StageNames          []string                 `json:"stageNames,omitempty"`
	TargetRefs          []string                 `json:"targetRefs,omitempty"`
	CreatedAt           time.Time                `json:"createdAt"`
	UpdatedAt           time.Time                `json:"updatedAt"`
}

// WorkflowPreviewTrigger binds an inline workflow to one pull request command.
type WorkflowPreviewTrigger struct {
	PreviewValues          map[string]map[string]any               `json:"previewValues,omitempty"`
	SourceTrustPolicy      string                                  `json:"sourceTrustPolicy"`
	LifetimeStartCommentID string                                  `json:"-"`
	LifetimeReportPending  bool                                    `json:"-"`
	TTL                    string                                  `json:"ttl"`
	ExpiresAt              *time.Time                              `json:"expiresAt,omitempty"`
	CleanupLeaseUntil      *time.Time                              `json:"-"`
	TemplateSource         *WorkflowPreviewTemplateGitSource       `json:"templateSource,omitempty"`
	LinkedPullRequests     map[string]int                          `json:"linkedPullRequests,omitempty"`
	SourceDefaults         map[string]WorkflowPreviewSourceDefault `json:"sourceDefaults,omitempty"`
	ID                     string                                  `json:"id"`
	TemplateID             string                                  `json:"templateId,omitempty"`
	ResourceID             string                                  `json:"resourceId"`
	GitHubAppID            string                                  `json:"githubAppId"`
	Repository             string                                  `json:"repository"`
	PullRequestNumber      int                                     `json:"pullRequestNumber"`
	Command                string                                  `json:"command"`
	AutoDeploy             bool                                    `json:"autoDeploy"`
	LiveReload             bool                                    `json:"liveReload"`
	LiveReloadCommentID    string                                  `json:"-"`
	MaxAutoRunsPerHour     int                                     `json:"maxAutoRunsPerHour"`
	PreviewURL             string                                  `json:"previewUrl,omitempty"`
	ReportCommentID        string                                  `json:"reportCommentId,omitempty"`
	CreatedAt              time.Time                               `json:"createdAt"`
	ClosedAt               *time.Time                              `json:"closedAt,omitempty"`
}

// WorkflowPreviewSourceDefault retains the configured reference before a PR
// comment pins a source to a linked commit.
type WorkflowPreviewSourceDefault struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch,omitempty"`
	Ref        string `json:"ref,omitempty"`
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
	SourceTrustPolicy  string                            `json:"sourceTrustPolicy"`
	CommentOnOpen      bool                              `json:"commentOnOpen"`
	TTL                string                            `json:"ttl"`
	WatchRepositories  []string                          `json:"watchRepositories,omitempty"`
	GitSource          *WorkflowPreviewTemplateGitSource `json:"gitSource,omitempty"`
	ID                 string                            `json:"id"`
	ConfigSourceID     string                            `json:"configSourceId"`
	GitHubAppID        string                            `json:"githubAppId"`
	Name               string                            `json:"name"`
	Repository         string                            `json:"repository"`
	Command            string                            `json:"command"`
	AutoDeploy         bool                              `json:"autoDeploy"`
	LiveReload         bool                              `json:"liveReload"`
	MaxAutoRunsPerHour int                               `json:"maxAutoRunsPerHour"`
	PreviewURL         string                            `json:"previewUrl"`
	Document           string                            `json:"document"`
	Active             bool                              `json:"active"`
	CreatedAt          time.Time                         `json:"createdAt"`
	UpdatedAt          time.Time                         `json:"updatedAt"`
}

type WorkflowSourceRevision struct {
	ContentHashes map[string]string `json:"contentHashes,omitempty"`
	Alias         string            `json:"alias"`
	Repository    string            `json:"repository"`
	Branch        string            `json:"branch"`
	CommitSHA     string            `json:"commitSha"`
	Path          string            `json:"path,omitempty"`
}

// WorkflowPullRequest captures the PR identity when a deployment is scheduled.
type WorkflowPullRequest struct {
	URL         string `json:"url,omitempty"`
	GitHubAppID string `json:"githubAppId"`
	Repository  string `json:"repository"`
	Number      int    `json:"number"`
	CommitSHA   string `json:"commitSha"`
}

// WorkflowInterruptedMessage identifies startup recovery, which is not a QA verdict.
const WorkflowInterruptedMessage = "Controller restarted while this work was in progress. Inspect the existing deployment and external side effects, then explicitly retry the workflow. No work was replayed."

type WorkflowReporting struct {
	StatusContext   string `json:"statusContext,omitempty" yaml:"statusContext,omitempty"`
	ReviewOnSuccess string `json:"reviewOnSuccess,omitempty" yaml:"reviewOnSuccess,omitempty"`
	ReviewOnFailure string `json:"reviewOnFailure,omitempty" yaml:"reviewOnFailure,omitempty"`
}

type WorkflowFeedbackTarget struct {
	WorkflowPullRequest
	Status     string `json:"status,omitempty"`
	Review     string `json:"review,omitempty"`
	ReviewID   int64  `json:"reviewId,omitempty"`
	Error      string `json:"error,omitempty"`
	SkipReason string `json:"skipReason,omitempty"`
}

type WorkflowFeedback struct {
	WorkflowReporting
	DeploymentID string                   `json:"deploymentId"`
	PreviewURL   string                   `json:"previewUrl,omitempty"`
	Targets      []WorkflowFeedbackTarget `json:"targets"`
	Complete     bool                     `json:"complete"`
}

type WorkflowRevision struct {
	// PreviewValues captures the Helm overlay for this run independently from
	// the mutable preview settings and the shared application document.
	PreviewValues map[string]map[string]any   `json:"previewValues,omitempty"`
	Checks        []WorkflowCheckReport       `json:"checks,omitempty"`
	SourceTrust   *PreviewSourceTrustDecision `json:"sourceTrust,omitempty"`
	PullRequests  []WorkflowPullRequest       `json:"pullRequests,omitempty"`
	Feedback      *WorkflowFeedback           `json:"feedback,omitempty"`

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
	ResourceID     string     `json:"resourceId,omitempty"`
	RevisionIDs    []string   `json:"revisionIds,omitempty"`
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

// WorkflowPreviewPanel is the single editable bot comment for a PR. ResourceID
// binds mirrored panels to the same preview, including after unlink or removal.
type WorkflowPreviewPanel struct {
	ActionError       string
	ID                string
	TemplateID        string
	GitHubAppID       string
	Repository        string
	PullRequestNumber int
	ResourceID        string
	CommentID         string
	Body              string
}
