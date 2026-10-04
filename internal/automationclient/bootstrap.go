package automationclient

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

type BootstrapRetryInput struct {
	Digest string `json:"digest"`
}

var bootstrapOperations = []Operation{
	{Name: "bootstraps_list", Method: "GET", Path: "/infrastructure/bootstrap", Description: "List installation plans visible under current infrastructure.inspect permissions, optionally filtered by project or server. Does not return installer claims or SSH credentials.", Fields: []string{"projectId", "serverId"}, Response: "target_bootstrap_list"},
	{Name: "bootstrap_get", Method: "GET", Path: "/infrastructure/bootstrap/{bootstrapId}", Description: "Inspect the original installation and refreshed enrollment/runtime evidence with current infrastructure.inspect access. Does not start installation.", Fields: []string{"bootstrapId"}, Required: []string{"bootstrapId"}, Response: "target_bootstrap"},
	{Name: "bootstrap_retry", Method: "POST", Path: "/infrastructure/bootstrap/{bootstrapId}/retry", Description: "Explicitly resume the same accepted SSH installation using its saved digest under current infrastructure.modify access. Preserves the target, artifact and credentials; never approves a new plan or allocates another machine. Inspect the original installation after response loss.", Fields: []string{"bootstrapId", "input"}, Required: []string{"bootstrapId", "input"}, Mutation: true, NonIdempotent: true, Destructive: true, InputSchema: "BootstrapRetryInput", Response: "target_bootstrap"},
}

func bootstrapInput(op Operation, args Arguments) (any, error) {
	if op.InputSchema != "BootstrapRetryInput" {
		return nil, nil
	}
	var input BootstrapRetryInput
	if decodeStrict(args.Input, &input) != nil || strings.TrimSpace(input.Digest) == "" {
		return nil, fmt.Errorf("supply the original accepted installation digest")
	}
	return input, nil
}

func bootstrapSchema(kind string) map[string]any {
	if kind != "BootstrapRetryInput" {
		return nil
	}
	return map[string]any{"type": "object", "properties": map[string]any{"digest": map[string]any{"type": "string", "minLength": 1}}, "required": []string{"digest"}, "additionalProperties": false}
}

func bootstrapResult(op Operation, args Arguments, out Result) Result {
	if op.Response != "target_bootstrap" && op.Response != "target_bootstrap_list" {
		return out
	}
	if op.Response == "target_bootstrap" {
		out.Continuation = &Continuation{Kind: "target_bootstrap", ID: args.BootstrapID}
	}
	if !out.OK {
		if op.Mutation && out.Error != nil {
			switch out.Error.Code {
			case "timeout", "cancelled", "unavailable", "invalid_response", "response_limit":
				out.Error.Title = "The installation response is incomplete. Inspect the original installation before another retry. No automatic retry occurred."
			}
		}
		return out
	}
	invalid := func() Result {
		out.OK = false
		out.Error = &Problem{Code: "invalid_response", Title: "The controller did not return the original installation identity. Inspect it before recovery."}
		return out
	}
	valid := func(item core.TargetBootstrap) bool {
		return identifier.MatchString(item.ID) && identifier.MatchString(item.ServerID) && item.State != "" && item.Digest != "" && (item.ProjectID == "" || identifier.MatchString(item.ProjectID))
	}
	if op.Response == "target_bootstrap_list" {
		var items []core.TargetBootstrap
		if json.Unmarshal(out.Data, &items) != nil || items == nil {
			return invalid()
		}
		for _, item := range items {
			if !valid(item) || args.ServerID != "" && item.ServerID != args.ServerID || args.ProjectID != "" && item.ProjectID != args.ProjectID {
				return invalid()
			}
		}
		return out
	}
	var item core.TargetBootstrap
	if json.Unmarshal(out.Data, &item) != nil || !valid(item) || item.ID != args.BootstrapID {
		return invalid()
	}
	if op.Mutation {
		var input BootstrapRetryInput
		_ = json.Unmarshal(args.Input, &input)
		if item.Digest != input.Digest {
			return invalid()
		}
	}
	out.Continuation.ResourceID = item.ServerID
	out.Continuation.ProjectID = item.ProjectID
	return out
}
