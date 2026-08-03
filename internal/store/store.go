package store

import (
	"context"

	"github.com/doout/dispatch/internal/core"
)

type Store interface {
	Close() error
	Migrate(context.Context) error
	SeedDemo(context.Context) error

	CreateProject(context.Context, core.Project) error
	ListProjects(context.Context) ([]core.Project, error)
	GetProject(context.Context, string) (core.Project, error)

	CreateServer(context.Context, core.Server) error
	ListServers(context.Context) ([]core.Server, error)
	GetServer(context.Context, string) (core.Server, error)

	CreateApp(context.Context, core.App) error
	ListApps(context.Context) ([]core.App, error)
	GetApp(context.Context, string) (core.App, error)

	CreateDeployment(context.Context, core.Deployment) error
	UpdateDeployment(context.Context, core.Deployment) error
	GetDeployment(context.Context, string) (core.Deployment, error)
	ListDeployments(context.Context, int) ([]core.Deployment, error)
	ActiveDeploymentForApp(context.Context, string) (*core.Deployment, error)

	AppendDeploymentLog(context.Context, core.DeploymentLog) error
	ListDeploymentLogs(context.Context, string, int64) ([]core.DeploymentLog, error)
}
