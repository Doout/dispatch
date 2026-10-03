package api

import (
	"context"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/backupoperations"
	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

// workloadBackupExecution supplies the API's vault, policy and runtime adapters
// to the operation service without exposing its HTTP admission dependencies.
type workloadBackupExecution struct {
	api     *API
	records store.WorkloadBackupStore
}

func (a workloadBackupExecution) GetWorkloadBackup(ctx context.Context, id string) (core.WorkloadBackup, error) {
	return a.records.GetWorkloadBackup(ctx, id)
}

func (a workloadBackupExecution) GetServer(ctx context.Context, id string) (core.Server, error) {
	return a.api.store.GetServer(ctx, id)
}

func (a workloadBackupExecution) CompleteWorkloadBackupOperation(ctx context.Context, b core.WorkloadBackup, op core.WorkloadBackupOperation) error {
	return a.records.CompleteWorkloadBackupOperation(ctx, b, op)
}

func (a workloadBackupExecution) DecodeOperation(op core.WorkloadBackupOperation) (core.WorkloadBackupRequest, error) {
	return a.api.decryptWorkloadBackup(op.ID, "operation", op.EncryptedInput)
}

func (a workloadBackupExecution) GrantObjects(ctx context.Context, b core.WorkloadBackup, storeID string, write, remove bool) (backupstore.Access, error) {
	return a.api.grantBackupObjects(ctx, b, storeID, write, remove)
}

func (a workloadBackupExecution) CheckPolicy(ctx context.Context, op core.WorkloadBackupOperation) error {
	return a.api.backupOperationPolicyAuthority(ctx, op)
}

func (a workloadBackupExecution) ExecutionGenerationMatches(ctx context.Context, op core.WorkloadBackupOperation) bool {
	return a.api.backupExecutionGenerationMatches(ctx, op)
}

func (a workloadBackupExecution) WithTarget(ctx context.Context, id string, fn func() error) error {
	return a.api.deploy.Storage.WithTarget(ctx, id, fn)
}

func (a workloadBackupExecution) Run(ctx context.Context, input core.WorkloadBackupRequest, server core.Server) (core.WorkloadBackupResult, error) {
	return a.api.runWorkloadBackup(ctx, input, server)
}

func (a workloadBackupExecution) DeleteOffsite(ctx context.Context, input core.WorkloadBackupRequest) (core.WorkloadBackupResult, error) {
	if a.api.workloadBackupBackend != nil {
		return a.api.workloadBackupBackend(ctx, input, core.Server{})
	}
	return deploy.DeleteOffsiteWorkloadBackup(ctx, nil, input)
}

func (a *API) runWorkloadBackup(ctx context.Context, input core.WorkloadBackupRequest, server core.Server) (core.WorkloadBackupResult, error) {
	if a.workloadBackupBackend != nil {
		return a.workloadBackupBackend(ctx, input, server)
	}
	if server.AgentNodeID == "" {
		return (deploy.DockerExecutor{WorkloadBackupDirectory: a.eventConfig.WorkloadBackupDirectory}).RunWorkloadBackup(ctx, input, server)
	}
	broker := a.runtimeBroker()
	if broker == nil {
		return core.WorkloadBackupResult{}, errors.New("remote encrypted backup execution is unavailable")
	}
	request := remoteruntime.NewWorkloadBackupRequest(input, server)
	id := "backup-" + input.OperationID
	if input.Action == "inspect" || input.Action == "reconcile" {
		id = ulid.Make().String()
	}
	job, err := broker.Submit(ctx, id, request)
	if err != nil {
		return core.WorkloadBackupResult{}, err
	}
	result, err := broker.Wait(ctx, job.ID, nil)
	if validation := request.ValidateWorkloadBackupResult(result); validation != nil {
		return core.WorkloadBackupResult{}, validation
	}
	if result.WorkloadBackup == nil {
		return core.WorkloadBackupResult{}, err
	}
	if err == nil && input.Action == "reconcile" {
		if data, ok := a.store.(interface {
			ReconcileWorkloadBackupRuntime(context.Context, string, string, string, time.Time) error
		}); ok {
			err = data.ReconcileWorkloadBackupRuntime(ctx, request.Service.Request.Run.ID, input.OperationID, job.ID, time.Now().UTC())
		}
	}
	return *result.WorkloadBackup, err
}

func (a *API) executeWorkloadBackupOperation(op core.WorkloadBackupOperation, recovering bool) {
	records, _ := a.workloadBackupStore()
	adapter := workloadBackupExecution{api: a, records: records}
	service := backupoperations.Service{Records: adapter, Authority: adapter, Material: adapter, Execution: adapter}
	if err := service.Execute(context.Background(), op, recovering); err != nil {
		a.logger.Error("Workload backup outcome could not be saved", "operation", op.ID)
	}
}
