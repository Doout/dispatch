package remoteruntime

import (
	"slices"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/runtimecontract"
)

func TestAgentCapabilitiesKeepWireOrderAndPublicContractSeparate(t *testing.T) {
	const wire = "deploy,inspect,logs,start,stop,rollback,destroy,provision_service,service_inspect,service_delete,workload_backup,workload_backup_inspect,workload_backup_offsite,workload_backup_offsite_inspect,workload_backup_retire,workload_backup_retire_inspect,storage_inspect,storage_delete,retention_inspect,retention_prune"
	if header := CapabilityHeader(); header != wire || len(header) != 311 {
		t.Fatalf("agent capability advertisement changed: %q", header)
	}
	values, err := ParseCapabilities(wire)
	if err != nil || len(values) != 20 {
		t.Fatalf("implemented capabilities exceed controller limits: %v", err)
	}
	operations := AgentCapabilities()
	operations[0] = "caller-mutation"
	if CapabilityHeader() != wire {
		t.Fatal("caller mutated future capability advertisements")
	}
	public := runtimecontract.Operations()
	if runtimecontract.APIVersion != "dispatch.runtime/v1" || !slices.Equal(public, []runtimecontract.Operation{"deploy", "inspect", "logs", "start", "stop", "rollback", "destroy"}) {
		t.Fatal("agent operations changed the public runtime contract")
	}
}

func TestParseCapabilitiesKeepsTokensAndExistingLimits(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		want         []string
		invalid      bool
	}{
		{name: "empty", header: "", want: []string{""}},
		{name: "literal tokens", header: "inspect,inspect,,future, inspect,", want: []string{"inspect", "inspect", "", "future", " inspect", ""}},
		{name: "20 entries", header: strings.Repeat("inspect,", 19) + "inspect", want: strings.Split(strings.Repeat("inspect,", 19)+"inspect", ",")},
		{name: "21 entries", header: strings.Repeat("inspect,", 20) + "inspect", invalid: true},
		{name: "512 bytes", header: strings.Repeat("x", 512), want: []string{strings.Repeat("x", 512)}},
		{name: "513 bytes", header: strings.Repeat("x", 513), invalid: true},
		{name: "512 UTF-8 bytes", header: strings.Repeat("é", 256), want: []string{strings.Repeat("é", 256)}},
		{name: "514 UTF-8 bytes", header: strings.Repeat("é", 257), invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values, err := ParseCapabilities(tc.header)
			if (err != nil) != tc.invalid || !tc.invalid && !slices.Equal(values, tc.want) {
				t.Fatalf("parsed %q as %q, error %v", tc.header, values, err)
			}
		})
	}
}
