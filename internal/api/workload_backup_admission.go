package api

import (
	"context"

	"github.com/doout/dispatch/internal/backupoperations"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/workloadbackup"
)

// The API supplies authenticated sources, encryption and runtime adapters. The
// admission service owns fresh archive construction and the two persistence paths.
type workloadBackupAdmission struct {
	api *API
}

func (a workloadBackupAdmission) LoadSource(ctx context.Context, id string) (backupoperations.Source, error) {
	record, accepted, server, storage, err := a.api.backupSource(ctx, id)
	return backupoperations.Source{Resource: record, Request: accepted.Request, Server: server, Storage: storage}, err
}

func (a workloadBackupAdmission) NewKey() (string, error) {
	return workloadbackup.Key()
}

func (a workloadBackupAdmission) EncodeCapture(id, kind string, input core.WorkloadBackupRequest) (string, error) {
	return a.api.encryptWorkloadBackup(id, kind, input)
}

func (a workloadBackupAdmission) DecodePolicy(p core.WorkloadBackupPolicy) (core.WorkloadBackupRequest, error) {
	return a.api.decryptWorkloadBackup(p.ID, "policy", p.EncryptedInput)
}

func (a workloadBackupAdmission) WithTarget(ctx context.Context, id string, fn func() error) error {
	return a.api.deploy.Storage.WithTarget(ctx, id, fn)
}

func (a workloadBackupAdmission) ValidateCapture(input core.WorkloadBackupRequest, server core.Server) error {
	return deploy.ValidateWorkloadBackupRequest(input, server)
}

func (a workloadBackupAdmission) CheckCapturePolicy(ctx context.Context, p core.WorkloadBackupPolicy) error {
	return a.api.backupPolicyAuthority(ctx, p)
}

func (a workloadBackupAdmission) Dispatch(op core.WorkloadBackupOperation, recovering bool) {
	go a.api.executeWorkloadBackupOperation(op, recovering)
}

func (a *API) backupAdmission() backupoperations.Admission {
	adapter := workloadBackupAdmission{api: a}
	return backupoperations.Admission{Sources: adapter, Material: adapter, Execution: adapter, Authority: adapter, Dispatch: adapter}
}
