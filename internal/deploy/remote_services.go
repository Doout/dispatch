package deploy

import (
	"context"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/remoteruntime"
)

func (e RemoteExecutor) Provision(ctx context.Context, request core.ServiceProvisionRequest, spec core.DockerServiceProvision, server core.Server) (map[string]string, error) {
	if server.AgentNodeID == "" {
		if local, ok := e.Local.(interface {
			Provision(context.Context, core.ServiceProvisionRequest, core.DockerServiceProvision, core.Server) (map[string]string, error)
		}); ok {
			return local.Provision(ctx, request, spec, server)
		}
		return nil, errors.New("Docker service provisioning is unavailable")
	}
	if e.Broker == nil {
		return nil, errors.New("encrypted remote service provisioning is unavailable")
	}
	job, err := e.Broker.Submit(ctx, "service-"+request.Run.ID, remoteruntime.NewServiceRequest(request, spec, server))
	if err != nil {
		return nil, err
	}
	result, err := e.Broker.Wait(ctx, job.ID, nil)
	if err != nil {
		return nil, err
	}
	if len(result.ServiceOutputs) == 0 {
		return nil, errors.New("agent returned no service connection")
	}
	return result.ServiceOutputs, nil
}
