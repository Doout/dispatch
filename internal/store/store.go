package store

import (
	"context"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type AdminCredential struct {
	Username     string
	PasswordHash string
	CreatedAt    time.Time
}

type Store interface {
	Close() error
	Migrate(context.Context) error
	SeedDemo(context.Context) error
	GetAdminCredential(context.Context) (AdminCredential, error)
	CreateAdminCredential(context.Context, AdminCredential) error
	CreateAdminSession(context.Context, string, time.Time, time.Time) error
	AdminSessionValid(context.Context, string, time.Time) (bool, error)
	CreateSecret(context.Context, core.Secret) error
	UpdateSecret(context.Context, core.Secret) error
	DeleteSecret(context.Context, string) error
	ListSecrets(context.Context) ([]core.Secret, error)
	GetSecret(context.Context, string) (core.Secret, error)
	CreateGitHubApp(context.Context, core.GitHubAppConnection) error
	UpdateGitHubApp(context.Context, core.GitHubAppConnection) error
	DeleteGitHubApp(context.Context, string) error
	ListGitHubApps(context.Context) ([]core.GitHubAppConnection, error)
	GetGitHubApp(context.Context, string) (core.GitHubAppConnection, error)

	CreateProject(context.Context, core.Project) error
	UpdateProject(context.Context, core.Project) error
	DeleteProject(context.Context, string) error
	ListProjects(context.Context) ([]core.Project, error)
	GetProject(context.Context, string) (core.Project, error)

	CreateServer(context.Context, core.Server) error
	UpdateServer(context.Context, core.Server) error
	DeleteServer(context.Context, string) error
	ListServers(context.Context) ([]core.Server, error)
	GetServer(context.Context, string) (core.Server, error)
	ReconcileLocalDockerServer(context.Context, bool) (*core.Server, error)
	CreateRelayWebhook(context.Context, core.RelayWebhook) error
	UpdateRelayWebhook(context.Context, core.RelayWebhook) error
	DeleteRelayWebhook(context.Context, string) error
	ListRelayWebhooks(context.Context, string) ([]core.RelayWebhook, error)
	GetRelayWebhook(context.Context, string) (core.RelayWebhook, error)
	GetRelayWebhookByRemoteID(context.Context, string, string) (core.RelayWebhook, error)

	CreateApp(context.Context, core.App) error
	UpdateApp(context.Context, core.App) error
	DeleteApp(context.Context, string) error
	AppHasDeployments(context.Context, string) (bool, error)
	ListApps(context.Context) ([]core.App, error)
	GetApp(context.Context, string) (core.App, error)

	CreateDeployment(context.Context, core.Deployment) error
	UpdateDeployment(context.Context, core.Deployment) error
	UpdateDeploymentOutputs(context.Context, string, map[string]string) error
	GetDeployment(context.Context, string) (core.Deployment, error)
	ListDeployments(context.Context, int) ([]core.Deployment, error)
	ActiveDeploymentForApp(context.Context, string) (*core.Deployment, error)

	AppendDeploymentLog(context.Context, core.DeploymentLog) error
	ListDeploymentLogs(context.Context, string, int64) ([]core.DeploymentLog, error)

	CreateEventTrigger(context.Context, core.EventTrigger) (core.EventTrigger, bool, error)
	UpdateEventTrigger(context.Context, core.EventTrigger) error
	ListEventTriggers(context.Context, string) ([]core.EventTrigger, error)
	DeleteEventTrigger(context.Context, string) error
	IncomingEventExists(context.Context, core.EventProvider, string) (bool, error)
	HasEventTrigger(context.Context, core.EventProvider, string, string, string) (bool, error)
	ProcessIncomingEvent(context.Context, core.IncomingEvent) (core.EventResult, error)
	ListPreviewEnvironments(context.Context, string) ([]core.PreviewEnvironment, error)
	GetPreviewEnvironment(context.Context, string) (core.PreviewEnvironment, error)
	TransitionPreviewEnvironment(context.Context, core.PreviewEnvironment, ...core.PreviewState) (bool, error)
	UpdatePreviewEnvironment(context.Context, core.PreviewEnvironment) error

	CreatePreviewGroup(context.Context, core.PreviewGroup) error
	UpdatePreviewGroup(context.Context, core.PreviewGroup) error
	DeletePreviewGroup(context.Context, string) error
	GetPreviewGroup(context.Context, string) (core.PreviewGroup, error)
	ListPreviewGroups(context.Context) ([]core.PreviewGroup, error)
	MatchingPreviewGroups(context.Context, core.EventProvider, string, string, string) ([]core.PreviewGroup, error)
	RecordPreviewGroupDelivery(context.Context, core.EventProvider, string, string, time.Time) (bool, error)
	CreatePreviewGroupRun(context.Context, core.PreviewGroupRun) error
	UpdatePreviewGroupRun(context.Context, core.PreviewGroupRun) error
	GetPreviewGroupRun(context.Context, string) (core.PreviewGroupRun, error)
	ListPreviewGroupRuns(context.Context, string) ([]core.PreviewGroupRun, error)
	FindPreviewGroupRunForPR(context.Context, string, string, int) (*core.PreviewGroupRun, error)
	ReplacePreviewGroupSources(context.Context, string, []core.PreviewGroupSource) error
	UpsertPreviewGroupRunComponent(context.Context, core.PreviewGroupRunComponent) error
	ClearPreviewGroupRunComponents(context.Context, string) error
	CreatePreviewGroupAttempt(context.Context, core.PreviewGroupAttempt) error
	UpdatePreviewGroupAttempt(context.Context, core.PreviewGroupAttempt) error
}
