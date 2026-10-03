package remoteruntime

import (
	"errors"
	"strings"

	"github.com/doout/dispatch/internal/runtimecontract"
)

const (
	MaxCapabilities    = 20
	MaxCapabilityBytes = 512
)

// AgentCapabilities lists implemented operations in their original wire order.
// The public dispatch.runtime/v1 manifest has its own smaller operation set.
func AgentCapabilities() []runtimecontract.Operation {
	return []runtimecontract.Operation{
		runtimecontract.Deploy, runtimecontract.Inspect, runtimecontract.Logs,
		runtimecontract.Start, runtimecontract.Stop, runtimecontract.Rollback, runtimecontract.Destroy,
		ProvisionService, ServiceInspect, ServiceDelete,
		WorkloadBackup, WorkloadBackupInspect, WorkloadBackupOffsite, WorkloadBackupOffsiteInspect,
		WorkloadBackupRetire, WorkloadBackupRetireInspect,
		StorageInspect, StorageDelete, RetentionInspect, RetentionPrune,
	}
}

func CapabilityHeader() string {
	operations := AgentCapabilities()
	values := make([]string, len(operations))
	for i, op := range operations {
		values[i] = string(op)
	}
	return strings.Join(values, ",")
}

// ParseCapabilities keeps unknown, duplicate and empty tokens intact. Negotiation
// checks exact operation names; parsing only enforces the existing wire limits.
func ParseCapabilities(header string) ([]string, error) {
	values := strings.Split(header, ",")
	if len(values) > MaxCapabilities || len(header) > MaxCapabilityBytes {
		return nil, errors.New("runtime capability list exceeds its limit")
	}
	return values, nil
}
