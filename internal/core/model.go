package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Server struct {
	ID         string                  `json:"id"`
	Name       string                  `json:"name"`
	Address    string                  `json:"address"`
	Runtime    string                  `json:"runtime"`
	State      string                  `json:"state"`
	AgentMode  string                  `json:"agentMode"`
	Kubernetes *KubernetesServerConfig `json:"kubernetes,omitempty"`
	CreatedAt  time.Time               `json:"createdAt"`
}

const (
	ServerRuntimeDocker     = "docker"
	ServerRuntimeKubernetes = "kubernetes"
	ServerRuntimeOpenShift  = "openshift"
)

func IsKubernetesRuntime(runtime string) bool {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	return runtime == ServerRuntimeKubernetes || runtime == ServerRuntimeOpenShift || runtime == "k8s"
}

type KubernetesServerConfig struct {
	KubeconfigPath             string                 `json:"kubeconfigPath,omitempty"`
	KubeconfigData             string                 `json:"-"`
	CertificateAuthorityData   string                 `json:"-"`
	KubeconfigStored           bool                   `json:"kubeconfigStored"`
	CertificateAuthorityStored bool                   `json:"certificateAuthorityStored"`
	Context                    string                 `json:"context,omitempty"`
	Namespace                  string                 `json:"namespace,omitempty"`
	OpenShift                  *OpenShiftServerConfig `json:"openShift,omitempty"`
}

type OpenShiftServerConfig struct {
	Managed                 bool       `json:"managed"`
	ServiceAccount          string     `json:"serviceAccount"`
	ServiceAccountNamespace string     `json:"serviceAccountNamespace"`
	TokenSecret             string     `json:"tokenSecret"`
	ConnectedAt             *time.Time `json:"connectedAt,omitempty"`
}

type BuildType string

const (
	BuildTypeDockerfile BuildType = "dockerfile"
	BuildTypeCompose    BuildType = "compose"
	BuildTypeHelm       BuildType = "helm"
)

type App struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"projectId"`
	ServerID       string    `json:"serverId"`
	Name           string    `json:"name"`
	SourceRepo     string    `json:"sourceRepo"`
	Branch         string    `json:"branch"`
	BuildType      BuildType `json:"buildType"`
	ContextPath    string    `json:"contextPath"`
	DockerfilePath string    `json:"dockerfilePath"`
	ComposePath    string    `json:"composePath"`
	ComposeContent string    `json:"-"`
	HelmChart      string    `json:"helmChart,omitempty"`
	HelmVersion    string    `json:"helmVersion,omitempty"`
	HelmRepository string    `json:"helmRepository,omitempty"`
	HelmValues     string    `json:"-"`
	// HelmGeneratedValues is populated transiently by a pre-deploy hook. It is
	// never persisted, returned by the API, or included in the stored spec digest.
	HelmGeneratedValues string `json:"-"`
	// HelmGroupValues is the final values layer for a generated preview-group
	// application. It is persisted for asynchronous execution but never exposed.
	HelmGroupValues string            `json:"-"`
	HookEnvironment map[string]string `json:"-"`
	Generated       bool              `json:"-"`
	Template        bool              `json:"template"`
	HelmNamespace   string            `json:"helmNamespace,omitempty"`
	HelmRelease     string            `json:"helmRelease,omitempty"`
	PreDeployHook   string            `json:"-"`
	PostDeployHook  string            `json:"-"`
	ContainerPort   int               `json:"containerPort"`
	Domain          string            `json:"domain"`
	State           string            `json:"state"`
	CreatedAt       time.Time         `json:"createdAt"`
}

func (a App) SpecDigest() string {
	payload, _ := json.Marshal(struct {
		ServerID        string
		SourceRepo      string
		Branch          string
		BuildType       BuildType
		ContextPath     string
		DockerfilePath  string
		ComposePath     string
		ComposeContent  string
		HelmChart       string
		HelmVersion     string
		HelmRepository  string
		HelmValues      string
		HelmNamespace   string
		HelmRelease     string
		PreDeployHook   string
		PostDeployHook  string
		HelmGroupValues string
		HookEnvironment map[string]string
		ContainerPort   int
		Domain          string
	}{a.ServerID, a.SourceRepo, a.Branch, a.BuildType, a.ContextPath, a.DockerfilePath, a.ComposePath, a.ComposeContent,
		a.HelmChart, a.HelmVersion, a.HelmRepository, a.HelmValues, a.HelmNamespace, a.HelmRelease,
		a.PreDeployHook, a.PostDeployHook, a.HelmGroupValues, a.HookEnvironment, a.ContainerPort, a.Domain})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type DeploymentState string

const (
	DeploymentQueued    DeploymentState = "queued"
	DeploymentFetching  DeploymentState = "fetching"
	DeploymentBuilding  DeploymentState = "building"
	DeploymentStarting  DeploymentState = "starting"
	DeploymentChecking  DeploymentState = "checking"
	DeploymentRouting   DeploymentState = "routing"
	DeploymentSucceeded DeploymentState = "succeeded"
	DeploymentFailed    DeploymentState = "failed"
	DeploymentCancelled DeploymentState = "cancelled"
)

func (s DeploymentState) Terminal() bool {
	return s == DeploymentSucceeded || s == DeploymentFailed || s == DeploymentCancelled
}

type Deployment struct {
	ID         string            `json:"id"`
	AppID      string            `json:"appId"`
	CommitSHA  string            `json:"commitSha"`
	SpecDigest string            `json:"specDigest"`
	State      DeploymentState   `json:"state"`
	Message    string            `json:"message"`
	CreatedAt  time.Time         `json:"createdAt"`
	StartedAt  *time.Time        `json:"startedAt,omitempty"`
	FinishedAt *time.Time        `json:"finishedAt,omitempty"`
	LeaseUntil *time.Time        `json:"leaseUntil,omitempty"`
	Outputs    map[string]string `json:"outputs,omitempty"`
	App        *App              `json:"app,omitempty"`
	Server     *Server           `json:"server,omitempty"`
}

type PreviewGroupState string

const (
	PreviewGroupRequested   PreviewGroupState = "requested"
	PreviewGroupDeploying   PreviewGroupState = "deploying"
	PreviewGroupReady       PreviewGroupState = "ready"
	PreviewGroupRollingBack PreviewGroupState = "rolling_back"
	PreviewGroupDegraded    PreviewGroupState = "degraded"
	PreviewGroupFailed      PreviewGroupState = "failed"
	PreviewGroupCleaning    PreviewGroupState = "cleaning"
	PreviewGroupClosed      PreviewGroupState = "closed"
)

func (s PreviewGroupState) Active() bool { return s != PreviewGroupClosed }

type PreviewGroupBinding struct {
	Source        string `json:"source"`
	HelmValuePath string `json:"helmValuePath"`
}

type PreviewGroupComponent struct {
	ID             string                `json:"id"`
	GroupID        string                `json:"groupId"`
	AppID          string                `json:"appId"`
	Alias          string                `json:"alias"`
	Repository     string                `json:"repository"`
	DefaultBranch  string                `json:"defaultBranch"`
	Entrypoint     bool                  `json:"entrypoint"`
	DependsOn      []string              `json:"dependsOn"`
	Bindings       []PreviewGroupBinding `json:"bindings"`
	PreDeployHook  string                `json:"preDeployHook,omitempty"`
	PostDeployHook string                `json:"postDeployHook,omitempty"`
}

type PreviewGroup struct {
	ID         string                  `json:"id"`
	Name       string                  `json:"name"`
	Command    string                  `json:"command"`
	Enabled    bool                    `json:"enabled"`
	Components []PreviewGroupComponent `json:"components"`
	CreatedAt  time.Time               `json:"createdAt"`
	UpdatedAt  time.Time               `json:"updatedAt"`
}

type PreviewGroupSource struct {
	ID              string     `json:"id"`
	RunID           string     `json:"runId"`
	GroupID         string     `json:"groupId"`
	ComponentID     string     `json:"componentId"`
	Alias           string     `json:"alias"`
	Repository      string     `json:"repository"`
	PullRequest     int        `json:"pullRequest,omitempty"`
	HeadRef         string     `json:"headRef"`
	SHA             string     `json:"sha"`
	BaseRef         string     `json:"baseRef,omitempty"`
	DefaultBranch   bool       `json:"defaultBranch"`
	StatusCommentID string     `json:"statusCommentId,omitempty"`
	ClosedAt        *time.Time `json:"closedAt,omitempty"`
}

type PreviewGroupRunComponent struct {
	ID             string            `json:"id"`
	RunID          string            `json:"runId"`
	ComponentID    string            `json:"componentId"`
	Alias          string            `json:"alias"`
	GeneratedAppID string            `json:"generatedAppId,omitempty"`
	DeploymentID   string            `json:"deploymentId,omitempty"`
	State          string            `json:"state"`
	URL            string            `json:"url,omitempty"`
	Outputs        map[string]string `json:"outputs,omitempty"`
	Message        string            `json:"message,omitempty"`
}

type PreviewGroupAttempt struct {
	ID              string               `json:"id"`
	RunID           string               `json:"runId"`
	Sequence        int                  `json:"sequence"`
	State           PreviewGroupState    `json:"state"`
	Configuration   PreviewGroup         `json:"configuration"`
	DesiredSources  []PreviewGroupSource `json:"desiredSources"`
	PreviousSources []PreviewGroupSource `json:"previousSources,omitempty"`
	Message         string               `json:"message,omitempty"`
	CreatedAt       time.Time            `json:"createdAt"`
	FinishedAt      *time.Time           `json:"finishedAt,omitempty"`
}

type PreviewGroupRun struct {
	ID                    string                     `json:"id"`
	GroupID               string                     `json:"groupId"`
	Slug                  string                     `json:"slug"`
	Namespace             string                     `json:"namespace"`
	State                 PreviewGroupState          `json:"state"`
	Message               string                     `json:"message,omitempty"`
	EntrypointURL         string                     `json:"entrypointUrl,omitempty"`
	Attempt               int                        `json:"attempt"`
	LastSuccessfulSources []PreviewGroupSource       `json:"lastSuccessfulSources,omitempty"`
	HookEnvironment       map[string]string          `json:"-"`
	Sources               []PreviewGroupSource       `json:"sources"`
	Components            []PreviewGroupRunComponent `json:"components"`
	Attempts              []PreviewGroupAttempt      `json:"attempts"`
	Group                 *PreviewGroup              `json:"group,omitempty"`
	CreatedAt             time.Time                  `json:"createdAt"`
	UpdatedAt             time.Time                  `json:"updatedAt"`
	ClosedAt              *time.Time                 `json:"closedAt,omitempty"`
}

type DeploymentLog struct {
	ID           int64     `json:"id"`
	DeploymentID string    `json:"deploymentId"`
	Level        string    `json:"level"`
	Message      string    `json:"message"`
	CreatedAt    time.Time `json:"createdAt"`
}

type EventProvider string

const EventProviderGitHub EventProvider = "github"

type EventKind string

const (
	EventKindPullRequestComment EventKind = "pull_request_comment"
	EventKindPullRequest        EventKind = "pull_request"
)

type EventTrigger struct {
	ID             string        `json:"id"`
	AppID          string        `json:"appId"`
	Provider       EventProvider `json:"provider"`
	Repository     string        `json:"repository"`
	Command        string        `json:"command"`
	Enabled        bool          `json:"enabled"`
	PreDeployHook  string        `json:"preDeployHook,omitempty"`
	PostDeployHook string        `json:"postDeployHook,omitempty"`
	CreatedAt      time.Time     `json:"createdAt"`
	UpdatedAt      time.Time     `json:"updatedAt"`
}

type IncomingEvent struct {
	ID                string        `json:"id"`
	Provider          EventProvider `json:"provider"`
	DeliveryID        string        `json:"deliveryId"`
	Kind              EventKind     `json:"kind"`
	Action            string        `json:"action"`
	Repository        string        `json:"repository"`
	PullRequestNumber int           `json:"pullRequestNumber"`
	HeadRef           string        `json:"headRef"`
	HeadSHA           string        `json:"headSha"`
	BaseRef           string        `json:"baseRef"`
	Actor             string        `json:"actor"`
	ActorAssociation  string        `json:"actorAssociation"`
	TrustedActor      bool          `json:"trustedActor"`
	Command           string        `json:"command,omitempty"`
	Arguments         string        `json:"arguments,omitempty"`
	SourceCommentID   string        `json:"sourceCommentId,omitempty"`
	ReceivedAt        time.Time     `json:"receivedAt"`
}

func (e IncomingEvent) HookEnvironment() map[string]string {
	values := map[string]string{
		"DISPATCH_EVENT_PROVIDER":            string(e.Provider),
		"DISPATCH_EVENT_KIND":                string(e.Kind),
		"DISPATCH_EVENT_ACTION":              e.Action,
		"DISPATCH_EVENT_REPOSITORY":          e.Repository,
		"DISPATCH_EVENT_PULL_REQUEST_NUMBER": strconv.Itoa(e.PullRequestNumber),
		"DISPATCH_EVENT_HEAD_REF":            e.HeadRef,
		"DISPATCH_EVENT_HEAD_SHA":            e.HeadSHA,
		"DISPATCH_EVENT_BASE_REF":            e.BaseRef,
		"DISPATCH_EVENT_ACTOR":               e.Actor,
		"DISPATCH_EVENT_COMMAND":             e.Command,
		"DISPATCH_EVENT_ARGUMENTS":           e.Arguments,
		"DISPATCH_EVENT_DELIVERY_ID":         e.DeliveryID,
		"DISPATCH_EVENT_SOURCE_COMMENT_ID":   e.SourceCommentID,
	}
	for key, value := range values {
		if strings.TrimSpace(value) == "" {
			delete(values, key)
		}
	}
	return values
}

type PreviewState string

const (
	PreviewRequested        PreviewState = "requested"
	PreviewStarting         PreviewState = "starting"
	PreviewDeploying        PreviewState = "deploying"
	PreviewReady            PreviewState = "ready"
	PreviewCleanupRequested PreviewState = "cleanup_requested"
	PreviewCleaning         PreviewState = "cleaning"
	PreviewClosed           PreviewState = "closed"
	PreviewFailed           PreviewState = "failed"
)

type PreviewEnvironment struct {
	ID                string            `json:"id"`
	TriggerID         string            `json:"triggerId"`
	TemplateAppID     string            `json:"templateAppId"`
	AppID             string            `json:"appId"`
	Provider          EventProvider     `json:"provider"`
	Repository        string            `json:"repository"`
	PullRequestNumber int               `json:"pullRequestNumber"`
	HeadRef           string            `json:"headRef"`
	HeadSHA           string            `json:"headSha"`
	BaseRef           string            `json:"baseRef"`
	DeliveryID        string            `json:"deliveryId"`
	SourceCommentID   string            `json:"sourceCommentId,omitempty"`
	StatusCommentID   string            `json:"statusCommentId,omitempty"`
	URL               string            `json:"url,omitempty"`
	DeploymentID      string            `json:"deploymentId,omitempty"`
	TriggeredBy       string            `json:"triggeredBy"`
	State             PreviewState      `json:"state"`
	Message           string            `json:"message,omitempty"`
	PreDeployHook     string            `json:"-"`
	PostDeployHook    string            `json:"-"`
	HookEnvironment   map[string]string `json:"-"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
	ClosedAt          *time.Time        `json:"closedAt,omitempty"`
}

type EventResult struct {
	Event            IncomingEvent        `json:"event"`
	Duplicate        bool                 `json:"duplicate"`
	Ignored          bool                 `json:"ignored"`
	Previews         []PreviewEnvironment `json:"previews"`
	PreviewGroupRuns []PreviewGroupRun    `json:"previewGroupRuns"`
}

type Overview struct {
	Demo             bool                 `json:"demo"`
	Projects         []Project            `json:"projects"`
	Servers          []Server             `json:"servers"`
	Apps             []App                `json:"apps"`
	Deployments      []Deployment         `json:"deployments"`
	EventTriggers    []EventTrigger       `json:"eventTriggers"`
	Previews         []PreviewEnvironment `json:"previews"`
	PreviewGroups    []PreviewGroup       `json:"previewGroups"`
	PreviewGroupRuns []PreviewGroupRun    `json:"previewGroupRuns"`
}
