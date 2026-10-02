package deploy

import (
	"context"
	"errors"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/doout/dispatch/internal/store"
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
	id, _ := ctx.Value(cleanupOperationKey{}).(string)
	if id != "" {
		existing, err := e.Broker.Store.GetRuntimeJob(ctx, id)
		if err == nil {
			if existing.AppID != app.ID || existing.ProjectID != app.ProjectID || existing.ServerID != server.ID || existing.Operation != string(runtimecontract.Destroy) {
				return errors.New("cleanup runtime ownership changed")
			}
			_, err = e.Broker.Wait(ctx, id, progress)
			return err
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	if id == "" {
		id = "cleanup-" + ulid.Make().String()
	}
	_, err := e.ExecuteRemote(ctx, id, runtimecontract.Destroy, core.Deployment{}, app, server, progress)
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
	result, waitErr := e.Broker.Wait(ctx, job.ID, progress)
	if result.Health != nil {
		if err := reportDeploymentHealth(ctx, d, *result.Health); err != nil {
			waitErr = errors.Join(waitErr, errors.New("remote health evidence could not be persisted"))
		}
	}
	if err := reportRemoteRoute(ctx, remoteruntime.NewRequest(op, d, app, server), result); err != nil {
		waitErr = errors.Join(waitErr, err)
	}
	return result, waitErr
}

func (e RemoteExecutor) Execute(ctx context.Context, op runtimecontract.Operation, d core.Deployment, app core.App, server core.Server, progress Progress) error {
	_, err := e.ExecuteRemote(ctx, "runtime-"+ulid.Make().String(), op, d, app, server, progress)
	return err
}

func reportRemoteRoute(ctx context.Context, request remoteruntime.Request, result remoteruntime.Result) error {
	if err := request.ValidateRoute(result.Route); err != nil {
		return err
	}
	if result.Route != nil {
		return ReportRoute(ctx, *result.Route)
	}
	return nil
}
