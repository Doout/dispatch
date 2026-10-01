package deploy

import (
	"context"
	"errors"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/oklog/ulid/v2"
)

// RemoteExecutor keeps local execution unchanged and uses durable typed jobs
// only for a target explicitly bound to an enrolled node.
type RemoteExecutor struct {
	Local  Executor
	Broker *remoteruntime.Broker
}

func (e RemoteExecutor) RuntimeCapabilities(app core.App, server core.Server) runtimecontract.Manifest {
	if server.AgentNodeID == "" {
		return executorCapabilities(e.Local, app, server)
	}
	if e.Broker == nil || server.Runtime != core.ServerRuntimeDocker || (app.BuildType != core.BuildTypeCompose && app.BuildType != core.BuildTypeDockerfile) {
		return runtimecontract.Describe("docker-agent", "outbound")
	}
	return runtimecontract.Describe("docker-agent", "outbound", runtimecontract.Deploy, runtimecontract.Inspect, runtimecontract.Logs, runtimecontract.Start, runtimecontract.Stop, runtimecontract.Rollback, runtimecontract.Destroy)
}

func (e RemoteExecutor) Deploy(ctx context.Context, d core.Deployment, app core.App, server core.Server, progress Progress) error {
	if server.AgentNodeID == "" {
		return e.Local.Deploy(ctx, d, app, server, progress)
	}
	_, err := e.ExecuteRemote(ctx, "deploy-"+d.ID, runtimecontract.Deploy, d, app, server, progress)
	return err
}

func (e RemoteExecutor) Cleanup(ctx context.Context, app core.App, server core.Server, progress Progress) error {
	if server.AgentNodeID == "" {
		cleaner, ok := e.Local.(CleanupExecutor)
		if !ok {
			return ErrCleanupUnsupported
		}
		return cleaner.Cleanup(ctx, app, server, progress)
	}
	_, err := e.ExecuteRemote(ctx, "cleanup-"+ulid.Make().String(), runtimecontract.Destroy, core.Deployment{}, app, server, progress)
	return err
}

func (e RemoteExecutor) ExecuteRemote(ctx context.Context, id string, op runtimecontract.Operation, d core.Deployment, app core.App, server core.Server, progress Progress) (remoteruntime.Result, error) {
	if server.AgentNodeID == "" || e.Broker == nil {
		return remoteruntime.Result{}, errors.New("the target has no enrolled runtime binding")
	}
	if err := e.RuntimeCapabilities(app, server).Check(ctx, op); err != nil {
		return remoteruntime.Result{}, err
	}
	job, err := e.Broker.Submit(ctx, id, remoteruntime.NewRequest(op, d, app, server))
	if err != nil {
		return remoteruntime.Result{}, err
	}
	return e.Broker.Wait(ctx, job.ID, progress)
}

func (e RemoteExecutor) Execute(ctx context.Context, op runtimecontract.Operation, d core.Deployment, app core.App, server core.Server, progress Progress) error {
	_, err := e.ExecuteRemote(ctx, "runtime-"+ulid.Make().String(), op, d, app, server, progress)
	return err
}
