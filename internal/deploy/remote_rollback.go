package deploy

import (
	"context"
	"errors"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/oklog/ulid/v2"
)

func (e RemoteExecutor) PreviewRuntimeRollback(ctx context.Context, d core.Deployment, app core.App, server core.Server) (RollbackPreview, error) {
	if server.AgentNodeID == "" {
		if local, ok := e.Local.(RuntimeRollbackExecutor); ok {
			return local.PreviewRuntimeRollback(ctx, d, app, server)
		}
		return RollbackPreview{}, errors.New("retained runtime rollback is unavailable")
	}
	request := remoteruntime.NewRequest(runtimecontract.Inspect, core.Deployment{}, app, server)
	request.SourceDeploymentID = d.ID
	job, err := e.Broker.Submit(ctx, "review-"+ulid.Make().String(), request)
	if err != nil {
		return RollbackPreview{}, err
	}
	result, err := e.Broker.Wait(ctx, job.ID, nil)
	if err != nil {
		return RollbackPreview{}, err
	}
	if result.Rollback == nil {
		return RollbackPreview{}, errors.New("agent did not return retained rollback evidence")
	}
	return *result.Rollback, nil
}

func (e RemoteExecutor) RollbackRuntime(ctx context.Context, d, source core.Deployment, app core.App, server core.Server, progress Progress) error {
	if server.AgentNodeID == "" {
		if local, ok := e.Local.(RuntimeRollbackExecutor); ok {
			return local.RollbackRuntime(ctx, d, source, app, server, progress)
		}
		return errors.New("retained runtime rollback is unavailable")
	}
	request := remoteruntime.NewRequest(runtimecontract.Rollback, d, app, server)
	request.SourceDeploymentID, request.ExpectedRuntime = source.ID, d.RuntimeReviewDigest
	job, err := e.Broker.Submit(ctx, "rollback-"+d.ID, request)
	if err != nil {
		return err
	}
	result, err := e.Broker.Wait(ctx, job.ID, progress)
	if reportErr := reportRemoteRoute(ctx, request, result); reportErr != nil {
		return reportErr
	}
	return err
}

func (e RemoteExecutor) CurrentRuntimeIdentity(ctx context.Context, app core.App, server core.Server) (string, error) {
	if server.AgentNodeID == "" {
		if local, ok := e.Local.(interface {
			CurrentRuntimeIdentity(context.Context, core.App, core.Server) (string, error)
		}); ok {
			return local.CurrentRuntimeIdentity(ctx, app, server)
		}
		return "", errors.New("runtime identity is unavailable")
	}
	result, err := e.ExecuteRemote(ctx, "inspect-"+ulid.Make().String(), runtimecontract.Inspect, core.Deployment{}, app, server, nil)
	if err != nil {
		return "", err
	}
	if result.RuntimeDigest == "" {
		return "", errors.New("agent did not return runtime identity evidence")
	}
	return result.RuntimeDigest, nil
}
