package automationclient

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

type ServerPowerInput struct {
	Action   string `json:"action"`
	Revision int64  `json:"revision"`
}

type ClonePromotionInput struct {
	Network     string `json:"network"`
	Revision    int64  `json:"revision"`
	ConfirmName string `json:"confirmName"`
}

var machineActionOperations = []Operation{
	{Name: "server_power", Method: "POST", Path: "/infrastructure/servers/{serverId}/power", Description: "Accept an explicitly selected start, stop or reboot for the original machine using its current revision and a stable retry key. Requires infrastructure.modify and provider support. Inspect its original operation after response loss.", Fields: []string{"serverId", "key", "input"}, Required: []string{"serverId", "key", "input"}, Mutation: true, Destructive: true, InputSchema: "ServerPowerInput", Response: "receipt_or_machine_action"},
	{Name: "clone_inspect", Method: "GET", Path: "/infrastructure/servers/{serverId}/clone", Description: "Inspect fresh provider identity, disk, isolation and enrollment evidence for the selected snapshot clone. Does not establish application integrity or promote it.", Fields: []string{"serverId"}, Required: []string{"serverId"}, Response: "clone_inspection"},
	{Name: "clone_promote", Method: "POST", Path: "/infrastructure/servers/{serverId}/promote", Description: "Accept explicit release of the verified isolated clone to the selected workload network using its current revision, exact name and a stable retry key. Requires infrastructure.modify and infrastructure.restore. Ancestry is retained; workloads and service bindings are not copied. Inspect the original operation after response loss.", Fields: []string{"serverId", "key", "input"}, Required: []string{"serverId", "key", "input"}, Mutation: true, Destructive: true, InputSchema: "ClonePromotionInput", Response: "receipt_or_machine_action"},
}

func machineActionInput(op Operation, args Arguments) (any, error) {
	switch op.InputSchema {
	case "ServerPowerInput":
		var input ServerPowerInput
		if decodeStrict(args.Input, &input) != nil || input.Revision < 1 || input.Action != "start" && input.Action != "stop" && input.Action != "reboot" {
			return nil, errors.New("supply an explicit start, stop or reboot and the current machine revision")
		}
		return input, nil
	case "ClonePromotionInput":
		var input ClonePromotionInput
		if decodeStrict(args.Input, &input) != nil || !identifier.MatchString(input.Network) || input.Revision < 1 || strings.TrimSpace(input.ConfirmName) == "" {
			return nil, errors.New("supply the workload network, current clone revision and exact confirmation name")
		}
		return input, nil
	}
	return nil, nil
}

func machineActionSchema(kind string) map[string]any {
	props := map[string]any{"revision": map[string]any{"type": "integer", "minimum": 1}}
	required := []string{"revision"}
	switch kind {
	case "ServerPowerInput":
		props["action"] = map[string]any{"type": "string", "enum": []string{"start", "stop", "reboot"}}
		required = append(required, "action")
	case "ClonePromotionInput":
		props["network"] = map[string]any{"type": "string", "minLength": 1}
		props["confirmName"] = map[string]any{"type": "string", "minLength": 1}
		required = append(required, "network", "confirmName")
	default:
		return nil
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func machineActionResult(op Operation, args Arguments, out Result) Result {
	if op.Response != "receipt_or_machine_action" && op.Response != "clone_inspection" {
		return out
	}
	out.Continuation = &Continuation{Kind: "managed_server", ID: args.ServerID, ResourceID: args.ServerID, Key: args.Key}
	if !out.OK {
		return out
	}
	invalid := func() Result {
		out.OK = false
		out.Error = &Problem{Code: "invalid_response", Title: "The controller did not return the original machine and operation. Inspect the saved machine and request key before recovery."}
		return out
	}
	if op.Response == "clone_inspection" {
		var result struct {
			Server struct {
				core.ManagedServer
			} `json:"server"`
			Resource struct {
				ID string `json:"id"`
			} `json:"resource"`
			Verified             *bool  `json:"verified"`
			ApplicationIntegrity string `json:"applicationIntegrity"`
		}
		if json.Unmarshal(out.Data, &result) != nil || result.Server.ID != args.ServerID || result.Server.SourceSnapshotID == "" || result.Server.ResourceID != result.Resource.ID || result.Resource.ID == "" || result.Verified == nil || result.ApplicationIntegrity != "unverified" {
			return invalid()
		}
		out.Continuation.ProjectID = result.Server.ProjectID
		return out
	}
	var receipt core.MutationReceipt
	if json.Unmarshal(out.Data, &receipt) != nil {
		return invalid()
	}
	action := "server.promote"
	if op.Name == "server_power" {
		var input ServerPowerInput
		_ = json.Unmarshal(args.Input, &input)
		action = "server." + input.Action
	}
	if receipt.OperationKind != "" {
		if receipt.OperationKind != "infrastructure_operation" || receipt.Action != action || receipt.ResourceID != args.ServerID || !identifier.MatchString(receipt.ID) || !identifier.MatchString(receipt.OperationID) || out.Location != "/api/v1/mutation-receipts/"+receipt.ID {
			return invalid()
		}
		out.Continuation = &Continuation{Kind: "receipt", ID: receipt.ID, ResourceID: receipt.ResourceID, OperationID: receipt.OperationID, ProjectID: receipt.ProjectID, Key: args.Key}
		return out
	}
	var accepted struct {
		Server    core.ManagedServer           `json:"server"`
		Operation core.InfrastructureOperation `json:"operation"`
	}
	if json.Unmarshal(out.Data, &accepted) != nil || accepted.Server.ID != args.ServerID || accepted.Operation.ServerID != args.ServerID || accepted.Operation.Action != action || !identifier.MatchString(accepted.Operation.ID) || accepted.Server.ResourceID == "" || accepted.Operation.ResourceID != accepted.Server.ResourceID {
		return invalid()
	}
	out.Continuation.ProjectID = accepted.Server.ProjectID
	out.Continuation.OperationID = accepted.Operation.ID
	return out
}
