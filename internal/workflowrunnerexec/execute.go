// Package workflowrunnerexec runs accepted requests inside the execution container.
package workflowrunnerexec

import (
	"context"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/kubeconfig"
	"github.com/doout/dispatch/internal/workflow"
	"github.com/doout/dispatch/internal/workflowrunner"
)

func Execute(ctx context.Context, request workflowrunner.Request, workspace string, progress func(string)) workflowrunner.Result {
	if request.StorageOperation != nil {
		return executeStorage(ctx, request, deploy.RuntimeStorage{})
	}
	if request.TargetInspection != nil {
		return executeTargetInspection(ctx, request, kubeconfig.InspectTarget)
	}
	if request.ServiceProvision != nil {
		return executeServiceProvision(ctx, request)
	}
	if request.Workflow != nil {
		return workflow.ExecuteWorkerJob(ctx, request, workspace, progress)
	}
	return executeDeployment(ctx, request, workspace, progress)
}
