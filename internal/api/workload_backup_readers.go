package api

import (
	"context"
	"errors"

	"github.com/doout/dispatch/internal/core"
)

type workloadBackupPolicyReader interface {
	GetWorkloadBackupPolicy(context.Context, string) (core.WorkloadBackupPolicy, error)
}

type workloadBackupSourceReader interface {
	GetServiceResource(context.Context, string) (core.ServiceResource, error)
}

type workloadBackupActorReader interface {
	GetServiceAccount(context.Context, string) (core.ServiceAccount, error)
	ListAutomationCredentials(context.Context, string) ([]core.AutomationCredential, error)
}

func (a *API) backupPolicyReader() (workloadBackupPolicyReader, error) {
	reader, ok := a.store.(workloadBackupPolicyReader)
	if !ok {
		return nil, errors.New("durable capture policies are unavailable")
	}
	return reader, nil
}
