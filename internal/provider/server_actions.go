package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
)

const (
	CapabilityPowerStart  = "server.start"
	CapabilityPowerStop   = "server.stop"
	CapabilityPowerReboot = "server.reboot"
	CapabilityPromote     = "server.promote"
	PowerRunning          = "running"
	PowerStopped          = "stopped"
	PowerTransitioning    = "transitioning"
	PowerUnknown          = "unknown"
)

// ServerActions is optional. Existing v1 providers retain their original contract.
type PowerProvider interface {
	PowerServer(context.Context, string, string, PowerServerRequest) (Operation, error)
}
type PromotionProvider interface {
	PromoteServer(context.Context, string, string, PromoteServerRequest) (Operation, error)
}
type PowerServerRequest struct {
	Action           string `json:"action"`
	ExpectedIdentity string `json:"expectedIdentity"`
}
type PromoteServerRequest struct {
	Network          string `json:"network"`
	ExpectedIdentity string `json:"expectedIdentity"`
}

// PromotionEvidence describes the completed release of this fresh clone only.
// Original restore evidence remains the record of sanitation before its first boot.
type PromotionEvidence struct {
	OperationID               string `json:"operationId"`
	Network                   string `json:"network"`
	IdentityDigest            string `json:"identityDigest"`
	QuarantineReleased        bool   `json:"quarantineReleased"`
	CopiedWorkloadsDisabled   bool   `json:"copiedWorkloadsDisabled"`
	ProductionBindingsCleared bool   `json:"productionBindingsCleared"`
}

func PowerCapability(action string) string {
	switch action {
	case "start":
		return CapabilityPowerStart
	case "stop":
		return CapabilityPowerStop
	case "reboot":
		return CapabilityPowerReboot
	}
	return ""
}
func ValidPowerState(state string) bool {
	return slices.Contains([]string{"", PowerRunning, PowerStopped, PowerTransitioning, PowerUnknown}, state)
}
func validDigest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == sha256.Size
}
func ValidatePowerRequest(in PowerServerRequest) error {
	if PowerCapability(in.Action) == "" || !validDigest(in.ExpectedIdentity) {
		return errors.New("power action and current machine identity are required")
	}
	return nil
}
func ValidatePromotionRequest(in PromoteServerRequest) error {
	if !ValidID(in.Network) || !validDigest(in.ExpectedIdentity) {
		return errors.New("promotion requires an explicit network and current clone identity")
	}
	return nil
}

// ServerIdentityDigest excludes changing power, network, address and operation
// status. It binds ownership, ancestry and the independently restored identities.
func ServerIdentityDigest(server Server) string {
	raw, _ := json.Marshal(struct {
		ID, Name, MachineIdentity, SSHIdentity string
		Labels                                 map[string]string
		Disks                                  []Disk
		Restore                                *RestoreEvidence
	}{server.ID, server.Name, server.MachineIdentity, server.SSHIdentity, server.Labels, server.Disks, server.Restore})
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
func VerifyPowerEvidence(server Server, operationID string, in PowerServerRequest) error {
	expected := PowerRunning
	if in.Action == "stop" {
		expected = PowerStopped
	}
	if ValidatePowerRequest(in) != nil || server.PowerState != expected || server.PowerOperationID != operationID || ServerIdentityDigest(server) != in.ExpectedIdentity {
		return errors.New("provider did not confirm the original machine power operation")
	}
	return nil
}
func VerifyPromotionEvidence(server Server, operationID string, in PromoteServerRequest) error {
	e := server.Promotion
	if ValidatePromotionRequest(in) != nil || e == nil || e.OperationID != operationID || e.Network != in.Network || server.Network != in.Network || e.IdentityDigest != in.ExpectedIdentity || ServerIdentityDigest(server) != in.ExpectedIdentity || !e.QuarantineReleased || !e.CopiedWorkloadsDisabled || !e.ProductionBindingsCleared || server.PowerState != PowerRunning {
		return errors.New("provider did not confirm the reviewed clone network release and identity")
	}
	return nil
}
func validateServerActionCapabilities(advertised map[string]bool) error {
	for _, capability := range []string{CapabilityPowerStart, CapabilityPowerStop, CapabilityPowerReboot, CapabilityPromote} {
		if advertised[capability] && (!advertised[CapabilityInspect] || !advertised[CapabilityOwnership]) {
			return errors.New("machine actions require inspection and ownership evidence")
		}
	}
	if advertised[CapabilityPromote] && !advertised[CapabilityRestore] {
		return errors.New("clone promotion requires isolated restore support")
	}
	return nil
}

// MutationCapability controls enablement checks independently from optional Go
// interfaces. Inspection remains available when a registration is disabled.
func MutationCapability(capability string) bool {
	return slices.Contains([]string{CapabilityCreate, CapabilityDelete, CapabilitySnapshotCreate, CapabilitySnapshotDelete, CapabilityRestore, CapabilityPowerStart, CapabilityPowerStop, CapabilityPowerReboot, CapabilityPromote}, capability)
}
