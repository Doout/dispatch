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
	CreateInitialOwner(context.Context, AdminCredential, core.User) error
	CreateAdminSession(context.Context, string, time.Time, time.Time) error
	CreateUserSession(context.Context, string, string, time.Time, time.Time) error
	DeleteSession(context.Context, string) error
	AdminSessionValid(context.Context, string, time.Time) (bool, error)
	SessionUser(context.Context, string, time.Time) (core.User, error)
	CreateUser(context.Context, core.User) error
	UpdateUser(context.Context, core.User) error
	DeleteUser(context.Context, string) error
	MergeUsers(context.Context, string, string) error
	GetUser(context.Context, string) (core.User, error)
	GetUserByUsername(context.Context, string) (core.User, error)
	GetUserByEmail(context.Context, string) (core.User, error)
	ListUsers(context.Context) ([]core.User, error)
	CountOwners(context.Context) (int, error)
	CreateAuthProvider(context.Context, core.AuthProvider) error
	UpdateAuthProvider(context.Context, core.AuthProvider) error
	DeleteAuthProvider(context.Context, string) error
	GetAuthProvider(context.Context, string) (core.AuthProvider, error)
	ListAuthProviders(context.Context) ([]core.AuthProvider, error)
	GetExternalIdentity(context.Context, string, string) (core.ExternalIdentity, error)
	FindExternalIdentitiesByLogin(context.Context, string) ([]core.ExternalIdentity, error)
	ListExternalIdentities(context.Context) ([]core.ExternalIdentity, error)
	UpsertExternalIdentity(context.Context, core.ExternalIdentity) error
	DeleteExternalIdentity(context.Context, string, string) error
	CreateTeam(context.Context, core.Team) error
	UpdateTeam(context.Context, core.Team) error
	DeleteTeam(context.Context, string) error
	GetTeam(context.Context, string) (core.Team, error)
	ListTeams(context.Context) ([]core.Team, error)
	ReplaceTeamMembers(context.Context, string, []core.TeamMember) error
	ListTeamMembers(context.Context) ([]core.TeamMember, error)
	UpsertRoleAssignment(context.Context, core.RoleAssignment) error
	DeleteRoleAssignment(context.Context, string) error
	ListRoleAssignments(context.Context) ([]core.RoleAssignment, error)
	CreateSecret(context.Context, core.Secret) error
	UpdateSecret(context.Context, core.Secret) error
	DeleteSecret(context.Context, string) error
	ListSecrets(context.Context) ([]core.Secret, error)
	GetSecret(context.Context, string) (core.Secret, error)
	CreateSecretStore(context.Context, core.SecretStore) error
	UpdateSecretStore(context.Context, core.SecretStore) error
	DeleteSecretStore(context.Context, string) error
	ListSecretStores(context.Context) ([]core.SecretStore, error)
	GetSecretStore(context.Context, string) (core.SecretStore, error)
	CreatePrivateNetwork(context.Context, core.PrivateNetwork) error
	UpdatePrivateNetwork(context.Context, core.PrivateNetwork) error
	DeletePrivateNetwork(context.Context, string) error
	ListPrivateNetworks(context.Context) ([]core.PrivateNetwork, error)
	GetPrivateNetwork(context.Context, string) (core.PrivateNetwork, error)
	GetPrivateNetworkByLaneway(context.Context, string, string) (core.PrivateNetwork, error)
	CreateLanewayApplication(context.Context, core.LanewayApplication) error
	GetLanewayApplication(context.Context, string) (core.LanewayApplication, error)
	GetActiveLanewayApplicationByAuthority(context.Context, string) (core.LanewayApplication, error)
	CreateLanewayAuthorizationTransaction(context.Context, core.LanewayAuthorizationTransaction) error
	ConsumeLanewayAuthorizationTransaction(context.Context, string, time.Time) (core.LanewayAuthorizationTransaction, error)
	AcquireLanewayRefreshLease(context.Context, string, string, time.Time, time.Time) error
	ReleaseLanewayRefreshLease(context.Context, string, string) error
	CreateEdgeJob(context.Context, core.EdgeJob) error
	LeaseEdgeJob(context.Context, string, time.Time, time.Duration) (*core.EdgeJob, error)
	CompleteEdgeJob(context.Context, string, string, string, string, string, time.Time) error
	GetEdgeJob(context.Context, string) (core.EdgeJob, error)
	DeleteEdgeJob(context.Context, string) error
	CreateGitHubApp(context.Context, core.GitHubAppConnection) error
	UpdateGitHubApp(context.Context, core.GitHubAppConnection) error
	DeleteGitHubApp(context.Context, string) error
	ListGitHubApps(context.Context) ([]core.GitHubAppConnection, error)
	GetGitHubApp(context.Context, string) (core.GitHubAppConnection, error)
	CreateConfigSource(context.Context, core.ConfigSource) error
	UpdateConfigSource(context.Context, core.ConfigSource) error
	DeleteConfigSource(context.Context, string) error
	GetConfigSource(context.Context, string) (core.ConfigSource, error)
	ListConfigSources(context.Context) ([]core.ConfigSource, error)
	CreateWorkflowResource(context.Context, core.WorkflowResource) error
	UpdateWorkflowResource(context.Context, core.WorkflowResource) error
	GetWorkflowResource(context.Context, string) (core.WorkflowResource, error)
	ListWorkflowResources(context.Context, string) ([]core.WorkflowResource, error)
	ReplaceWorkflowResources(context.Context, core.ConfigSource, []core.WorkflowResource) error
	CreateWorkflowEvent(context.Context, core.WorkflowEvent) (bool, error)
	UpdateWorkflowEvent(context.Context, core.WorkflowEvent) error
	CreateWorkflowRevision(context.Context, core.WorkflowRevision) error
	UpdateWorkflowRevision(context.Context, core.WorkflowRevision) error
	GetWorkflowRevision(context.Context, string) (core.WorkflowRevision, error)
	ListWorkflowRevisions(context.Context, string, int) ([]core.WorkflowRevision, error)
	CreateWorkflowJobResult(context.Context, core.WorkflowJobResult) error
	UpdateWorkflowJobResult(context.Context, core.WorkflowJobResult) error
	FindWorkflowJobResult(context.Context, string, string, string) (core.WorkflowJobResult, error)
	ListWorkflowJobResults(context.Context, string) ([]core.WorkflowJobResult, error)
	CreateWorkflowStageRun(context.Context, core.WorkflowStageRun) error
	UpdateWorkflowStageRun(context.Context, core.WorkflowStageRun) error
	GetWorkflowStageRun(context.Context, string) (core.WorkflowStageRun, error)
	ListWorkflowStageRuns(context.Context, string) ([]core.WorkflowStageRun, error)

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
	UpdateDeploymentSnapshot(context.Context, string, core.DeploymentSnapshot) error
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
