package api

import "context"

// RecoverWorkflowWork runs synchronously before accepting controller traffic.
func (a *API) RecoverWorkflowWork(ctx context.Context) error {
	if a.workflows == nil {
		return nil
	}
	return a.workflows.RecoverInterrupted(ctx)
}
