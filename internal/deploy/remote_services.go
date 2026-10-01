package deploy

import (
	"context"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/oklog/ulid/v2"
	"time"
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
	id := "service-" + request.Run.ID
	if request.OperationID != "" {
		id = request.OperationID
	}
	job, err := e.Broker.Submit(ctx, id, remoteruntime.NewServiceRequest(request, spec, server))
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

func (e RemoteExecutor) InspectServiceResource(ctx context.Context, req core.ServiceProvisionRequest, spec core.DockerServiceProvision, server core.Server) (core.ServiceResourceInspection, error) {
	if server.AgentNodeID == "" {
		return (DockerExecutor{}).InspectServiceResource(ctx, req, server)
	}
	if e.Broker == nil {
		return core.ServiceResourceInspection{}, errors.New("remote service inspection is unavailable")
	}
	request := remoteruntime.NewServiceRequest(req, spec, server)
	request.Operation = remoteruntime.ServiceInspect
	job, err := e.Broker.Submit(ctx, ulid.Make().String(), request)
	if err != nil {
		return core.ServiceResourceInspection{}, err
	}
	result, err := e.Broker.Wait(ctx, job.ID, nil)
	if err != nil {
		return core.ServiceResourceInspection{}, err
	}
	if err = request.ValidateServiceResult(result); err != nil {
		return core.ServiceResourceInspection{}, err
	}
	if reconciler, ok := e.Broker.Store.(interface {
		ReconcileServiceRuntimeJobs(context.Context, string, string, time.Time) error
	}); ok {
		if err = reconciler.ReconcileServiceRuntimeJobs(ctx, req.Run.ID, job.ID, time.Now().UTC()); err != nil {
			return core.ServiceResourceInspection{}, err
		}
	}
	return *result.ServiceResource, nil
}
func (e RemoteExecutor) DeleteServiceResource(ctx context.Context, req core.ServiceProvisionRequest, spec core.DockerServiceProvision, server core.Server, expected, operation string) error {
	if server.AgentNodeID == "" {
		return (DockerExecutor{}).DeleteServiceResource(ctx, req, server, expected)
	}
	if e.Broker == nil {
		return errors.New("remote service cleanup is unavailable")
	}
	request := remoteruntime.NewServiceRequest(req, spec, server)
	request.Operation = remoteruntime.ServiceDelete
	request.Service.ExpectedResourceID = expected
	job, err := e.Broker.Submit(ctx, operation, request)
	if err != nil {
		return err
	}
	_, err = e.Broker.Wait(ctx, job.ID, nil)
	return err
}
