package api

import "context"

// RecoverWorkflowWork runs synchronously before accepting controller traffic.
func (a *API) RecoverWorkflowWork(ctx context.Context) error {
	if recovery, ok := a.store.(interface{ RecoverInterruptedServiceProvisionRuns(context.Context) error }); ok {
		if err := recovery.RecoverInterruptedServiceProvisionRuns(ctx); err != nil {
			return err
		}
	}
	if a.workflows == nil {
		return nil
	}
	return a.workflows.RecoverInterrupted(ctx)
}
