package automationclient

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

// ApplicationUpdateInput excludes owner-only repository credential references.
type ApplicationUpdateInput struct {
	ExpectedSpecDigest string          `json:"expectedSpecDigest"`
	SourceRepo         *string         `json:"sourceRepo,omitempty"`
	Branch             *string         `json:"branch,omitempty"`
	BuildType          *core.BuildType `json:"buildType,omitempty"`
	ContextPath        *string         `json:"contextPath,omitempty"`
	DockerfilePath     *string         `json:"dockerfilePath,omitempty"`
	ComposePath        *string         `json:"composePath,omitempty"`
	ComposeContent     *string         `json:"composeContent,omitempty"`
	Domain             *string         `json:"domain,omitempty"`
	ContainerPort      *int            `json:"containerPort,omitempty"`
	HelmChart          *string         `json:"helmChart,omitempty"`
	HelmVersion        *string         `json:"helmVersion,omitempty"`
	HelmRepository     *string         `json:"helmRepository,omitempty"`
}

var appUpdateOperations = []Operation{
	{Name: "app_update", Method: "PATCH", Path: "/apps/{appId}", Description: "Edit explicitly supplied saved inputs on an idle manually managed application using expectedSpecDigest from app_get. Preserves identity and history and does not deploy. Inspect the same application after response loss. Repository credential references are excluded.", Fields: []string{"appId", "input"}, Required: []string{"appId", "input"}, Mutation: true, NonIdempotent: true, InputSchema: "ApplicationUpdateInput", Response: "application_update"},
}

func applicationUpdateSchema(kind string) map[string]any {
	if kind != "ApplicationUpdateInput" {
		return nil
	}
	props := map[string]any{"expectedSpecDigest": map[string]any{"type": "string", "pattern": "^sha256:[a-f0-9]{64}$"}}
	for _, key := range []string{"sourceRepo", "branch", "contextPath", "dockerfilePath", "composePath", "composeContent", "domain", "helmChart", "helmVersion", "helmRepository"} {
		props[key] = map[string]any{"type": "string"}
	}
	props["composeContent"].(map[string]any)["maxLength"] = 512 * 1024
	props["buildType"] = map[string]any{"type": "string", "enum": []string{"dockerfile", "compose", "helm"}}
	props["containerPort"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 65535}
	return map[string]any{"type": "object", "properties": props, "required": []string{"expectedSpecDigest"}, "minProperties": 2, "additionalProperties": false}
}

func applicationUpdateInput(op Operation, args Arguments) (any, error) {
	if op.InputSchema != "ApplicationUpdateInput" {
		return nil, nil
	}
	var input ApplicationUpdateInput
	if decodeStrict(args.Input, &input) != nil || !applicationSpecDigest(input.ExpectedSpecDigest) {
		return nil, errors.New("Supply expectedSpecDigest from app_get and supported application configuration fields")
	}
	if input.SourceRepo == nil && input.Branch == nil && input.BuildType == nil && input.ContextPath == nil && input.DockerfilePath == nil &&
		input.ComposePath == nil && input.ComposeContent == nil && input.Domain == nil && input.ContainerPort == nil && input.HelmChart == nil &&
		input.HelmVersion == nil && input.HelmRepository == nil {
		return nil, errors.New("Supply at least one explicit configuration change")
	}
	if input.BuildType != nil && *input.BuildType != core.BuildTypeDockerfile && *input.BuildType != core.BuildTypeCompose && *input.BuildType != core.BuildTypeHelm ||
		input.ContainerPort != nil && (*input.ContainerPort < 0 || *input.ContainerPort > 65535) || input.ComposeContent != nil && len(*input.ComposeContent) > 512*1024 {
		return nil, errors.New("Use a supported build type, valid container port and Compose content under 512 KB")
	}
	return input, nil
}

func applicationSpecDigest(value string) bool {
	digest, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return strings.HasPrefix(value, "sha256:") && err == nil && len(digest) == 32 && value == strings.ToLower(value)
}

func applicationUpdateResult(op Operation, args Arguments, out Result) Result {
	if op.Response != "application_update" {
		return out
	}
	out.Continuation = &Continuation{Kind: "application", ID: args.AppID}
	if !out.OK {
		if out.Error != nil {
			switch out.Error.Code {
			case "timeout", "cancelled", "unavailable", "invalid_response", "response_limit":
				out.Error.Title = "The response is incomplete. Inspect the same application's saved configuration before submitting another update. No automatic retry occurred."
			}
		}
		return out
	}
	var result struct {
		ID         string `json:"id"`
		SpecDigest string `json:"specDigest"`
	}
	if json.Unmarshal(out.Data, &result) != nil || result.ID != args.AppID || !applicationSpecDigest(result.SpecDigest) {
		out.OK = false
		out.Error = &Problem{Code: "invalid_response", Title: "The response does not identify the original application and saved configuration. Inspect that application before submitting another update."}
	}
	return out
}
