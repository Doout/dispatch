package remoteruntime

import (
	"context"
	"errors"
)

// A workflow worker and the typed runtime share an enrollment. Both queues must
// enforce its current mode and project before releasing or accepting a job.
func (b *Broker) validateWorkerScope(ctx context.Context, request Request) error {
	node, err := b.Store.GetPrivateNetwork(ctx, request.Server.AgentNodeID)
	if err != nil {
		return errors.New("runtime worker registration is unavailable")
	}
	switch node.Config["workflowMode"] {
	case "":
		// Existing runtime agents have no workflow worker configuration.
		return nil
	case "tenant":
	default:
		return errors.New("this worker is not enabled for typed runtime execution")
	}
	project := node.Config["workflowProjectId"]
	if project == "" {
		return nil
	}
	owner := request.Application.ProjectID
	if request.Operation == StorageInspect {
		// Storage inventory covers a whole target and has no application owner.
		// Only a target assigned to this project can use a restricted worker.
		server, err := b.Store.GetServer(ctx, request.Server.ID)
		if err != nil || server.AgentNodeID != node.ID {
			return errors.New("runtime worker target is unavailable")
		}
		owner = server.ProjectID
	}
	if owner != project {
		return errors.New("runtime operation is outside the worker's project")
	}
	return nil
}
