package automationclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

// ApplicationInput deliberately excludes global credential references and hooks.
// Scoped callers can bind approved services after creating the application.
type ApplicationInput struct {
	ProjectID          string             `json:"projectId"`
	ServerID           string             `json:"serverId"`
	Name               string             `json:"name"`
	SourceRepo         string             `json:"sourceRepo,omitempty"`
	Branch             string             `json:"branch,omitempty"`
	BuildType          string             `json:"buildType,omitempty"`
	ContextPath        string             `json:"contextPath,omitempty"`
	DockerfilePath     string             `json:"dockerfilePath,omitempty"`
	ComposePath        string             `json:"composePath,omitempty"`
	ComposeContent     string             `json:"composeContent,omitempty"`
	Domain             string             `json:"domain,omitempty"`
	ContainerPort      int                `json:"containerPort,omitempty"`
	Template           bool               `json:"template,omitempty"`
	HelmChart          string             `json:"helmChart,omitempty"`
	HelmVersion        string             `json:"helmVersion,omitempty"`
	HelmRepository     string             `json:"helmRepository,omitempty"`
	HelmValues         string             `json:"helmValues,omitempty"`
	HelmValueOverrides map[string]any     `json:"helmValueOverrides,omitempty"`
	HelmNamespace      string             `json:"helmNamespace,omitempty"`
	HelmRelease        string             `json:"helmRelease,omitempty"`
	HealthPolicy       *core.HealthPolicy `json:"healthPolicy,omitempty"`
}

type HelmValuesInput struct {
	Overrides map[string]any `json:"overrides"`
}

type DeploymentRollbackInput struct {
	ConfirmDeploymentID         string `json:"confirmDeploymentId"`
	ExpectedCurrentDeploymentID string `json:"expectedCurrentDeploymentId"`
	ConfirmDatabaseNotReverted  bool   `json:"confirmDatabaseNotReverted"`
	ExpectedReviewDigest        string `json:"expectedReviewDigest"`
}

type ServerAdoptionInput struct {
	ResourceID  string `json:"resourceId"`
	Revision    int64  `json:"revision"`
	ConfirmName string `json:"confirmName"`
}

var workflowOperations = append(append([]Operation{
	{Name: "app_get", Method: "GET", Path: "/apps/{appId}", Description: "Inspect the accepted application by its durable resource ID using current project access. Credential references remain redacted for scoped callers.", Fields: []string{"appId"}, Required: []string{"appId"}},
	{Name: "app_create", Method: "POST", Path: "/apps", Description: "Create an application on a ready assigned target with explicit source configuration and a stable retry key. Global credentials and hooks are excluded.", Fields: []string{"key", "input"}, Required: []string{"key", "input"}, Mutation: true, InputSchema: "ApplicationInput", Response: "receipt_or_application"},
	{Name: "app_sync", Method: "GET", Path: "/apps/{appId}/sync", Description: "Inspect saved and applied application configuration without changing or deploying it.", Fields: []string{"appId"}, Required: []string{"appId"}},
	{Name: "app_deployments", Method: "GET", Path: "/apps/{appId}/deployment-history", Description: "Inspect the latest 50 saved deployment attempts for an application. Use original IDs when recovering an interrupted mutation.", Fields: []string{"appId"}, Required: []string{"appId"}},
	{Name: "app_bindings_get", Method: "GET", Path: "/apps/{appId}/service-bindings", Description: "Inspect application service mappings without returning credentials.", Fields: []string{"appId"}, Required: []string{"appId"}},
	{Name: "app_bindings_set", Method: "PUT", Path: "/apps/{appId}/service-bindings", Description: "Replace explicitly supplied service mappings. Requires project.configure and an idle editable application. Does not deploy; inspect current bindings after response loss.", Fields: []string{"appId", "input"}, Required: []string{"appId", "input"}, Mutation: true, NonIdempotent: true, InputSchema: "ServiceBindings", Response: "application_configuration"},
	{Name: "app_helm_values_get", Method: "GET", Path: "/apps/{appId}/helm-values", Description: "Inspect structured Helm overrides for an authorized application.", Fields: []string{"appId"}, Required: []string{"appId"}},
	{Name: "app_helm_values_set", Method: "PUT", Path: "/apps/{appId}/helm-values", Description: "Replace supplied structured Helm overrides on an idle editable Helm application. Does not deploy; inspect current values after response loss.", Fields: []string{"appId", "input"}, Required: []string{"appId", "input"}, Mutation: true, NonIdempotent: true, InputSchema: "HelmValuesInput", Response: "application_configuration"},
	{Name: "app_health_get", Method: "GET", Path: "/apps/{appId}/health-policy", Description: "Inspect the application's current normalized deployment health policy.", Fields: []string{"appId"}, Required: []string{"appId"}},
	{Name: "app_health_set", Method: "PUT", Path: "/apps/{appId}/health-policy", Description: "Save explicitly supplied health checks for future deployments. Requires project.configure and an idle application; inspect the policy after response loss.", Fields: []string{"appId", "input"}, Required: []string{"appId", "input"}, Mutation: true, NonIdempotent: true, InputSchema: "HealthPolicy", Response: "application_configuration"},
	{Name: "deployment_cancel", Method: "POST", Path: "/deployments/{deploymentId}/cancel", Description: "Request cancellation of the selected original deployment. Requires deployment.cancel. Inspect its state; cancellation does not undo external changes.", Fields: []string{"deploymentId"}, Required: []string{"deploymentId"}, Mutation: true, NonIdempotent: true, Destructive: true, Response: "deployment_action"},
	{Name: "deployment_rollback_review", Method: "POST", Path: "/deployments/{deploymentId}/rollback-preview", Description: "Review rollback to a retained deployment and its current release and artifact digest. Does not revert database migrations or accept rollback.", Fields: []string{"deploymentId"}, Required: []string{"deploymentId"}},
	{Name: "deployment_rollback", Method: "POST", Path: "/deployments/{deploymentId}/rollback", Description: "Accept exact reviewed rollback evidence with a stable retry key and explicit acknowledgment that database migrations are not reverted.", Fields: []string{"deploymentId", "key", "input"}, Required: []string{"deploymentId", "key", "input"}, Mutation: true, Destructive: true, InputSchema: "DeploymentRollbackInput", Response: "receipt_or_rollback"},
	{Name: "server_retry", Method: "POST", Path: "/infrastructure/operations/{operationId}/retry", Description: "Resume the selected original infrastructure operation under current grants and its existing request identity and deadline. Does not create a replacement request; inspect operation history if interrupted.", Fields: []string{"serverId", "operationId"}, Required: []string{"serverId", "operationId"}, Mutation: true, NonIdempotent: true, Destructive: true, Response: "infrastructure_action"},
	{Name: "server_cancel", Method: "POST", Path: "/infrastructure/operations/{operationId}/cancel", Description: "Request cancellation of the original infrastructure operation. An uncertain allocation retains ownership and quota; inspect before adoption or further recovery.", Fields: []string{"serverId", "operationId"}, Required: []string{"serverId", "operationId"}, Mutation: true, NonIdempotent: true, Destructive: true, Response: "infrastructure_action"},
	{Name: "server_adopt", Method: "POST", Path: "/infrastructure/servers/{serverId}/adopt", Description: "Inspect and adopt the original reviewed provider resource using its explicit resource ID, current revision and exact confirmation name. Foreign ownership or active leases block adoption.", Fields: []string{"serverId", "input"}, Required: []string{"serverId", "input"}, Mutation: true, NonIdempotent: true, Destructive: true, InputSchema: "ServerAdoptionInput", Response: "managed_server"},
	{Name: "server_enrollment", Method: "POST", Path: "/infrastructure/servers/{serverId}/enrollment", Description: "Explicitly issue a short-lived single-use enrollment token for an allocated unenrolled machine. Requires infrastructure.modify; cannot replace an enrolled identity or refresh an approved bootstrap. Store the returned token privately. No automatic retry occurs.", Fields: []string{"serverId"}, Required: []string{"serverId"}, Mutation: true, NonIdempotent: true, Destructive: true, Response: "server_enrollment"},
}, serviceClientOperations...), backupPolicyClientOperations...)

func isWorkflowOperation(name string) bool {
	for _, op := range workflowOperations {
		if name == op.Name {
			return true
		}
	}
	return false
}

func workflowInput(op Operation, args Arguments) (any, error) {
	if op.InputSchema == "BackupPolicyCreateInput" || op.InputSchema == "BackupPolicyUpdateInput" {
		return backupPolicyClientInput(op, args)
	}
	if op.InputSchema == "ServiceProvisionInput" {
		return serviceClientInput(op, args)
	}
	fail := func(message string) (any, error) { return nil, errors.New(message) }
	switch op.InputSchema {
	case "ApplicationInput":
		var input ApplicationInput
		if decodeStrict(args.Input, &input) != nil || !identifier.MatchString(input.ProjectID) || !identifier.MatchString(input.ServerID) || strings.TrimSpace(input.Name) == "" {
			return fail("Supply a project, assigned ready target, application name and explicit source configuration")
		}
		if input.SourceRepo == "" && input.ComposeContent == "" && !(input.BuildType == "helm" && input.HelmChart != "") {
			return fail("Supply a repository, Compose content or Helm chart")
		}
		if input.BuildType != "" && input.BuildType != "dockerfile" && input.BuildType != "compose" && input.BuildType != "helm" || input.ContainerPort < 0 || input.ContainerPort > 65535 || input.HelmValues != "" && input.HelmValueOverrides != nil {
			return fail("Use a supported build type, valid container port and one Helm values format")
		}
		if input.HealthPolicy != nil {
			if _, err := core.NormalizeHealthPolicy(*input.HealthPolicy); err != nil {
				return fail("Supply a valid deployment health policy")
			}
		}
		return input, nil
	case "ServiceBindings":
		var input []core.ServiceBinding
		if decodeStrict(args.Input, &input) != nil || input == nil {
			return fail("Supply an explicit service-binding array; use [] to clear bindings")
		}
		return input, nil
	case "HelmValuesInput":
		var input HelmValuesInput
		if decodeStrict(args.Input, &input) != nil || input.Overrides == nil {
			return fail("Supply an explicit overrides object; use {} to clear Helm overrides")
		}
		return input, nil
	case "HealthPolicy":
		var input core.HealthPolicy
		if decodeStrict(args.Input, &input) != nil || string(args.Input) == "null" {
			return fail("Supply an explicit health policy")
		}
		if _, err := core.NormalizeHealthPolicy(input); err != nil {
			return fail("Supply a valid deployment health policy")
		}
		return input, nil
	case "DeploymentRollbackInput":
		var input DeploymentRollbackInput
		if decodeStrict(args.Input, &input) != nil || input.ConfirmDeploymentID != args.DeploymentID || !identifier.MatchString(input.ExpectedCurrentDeploymentID) || !input.ConfirmDatabaseNotReverted || strings.TrimSpace(input.ExpectedReviewDigest) == "" {
			return fail("Supply the reviewed deployment, current release, digest and explicit database acknowledgment")
		}
		return input, nil
	case "ServerAdoptionInput":
		var input ServerAdoptionInput
		if decodeStrict(args.Input, &input) != nil || !identifier.MatchString(input.ResourceID) || input.Revision < 1 || strings.TrimSpace(input.ConfirmName) == "" {
			return fail("Supply the original provider resource, current server revision and exact confirmation name")
		}
		return input, nil
	}
	return nil, nil
}

func workflowContinuation(op Operation, args Arguments, out Result) Result {
	if op.Response == "" {
		return out
	}
	kind, id, operation, project := "application", args.AppID, "", ""
	switch op.Response {
	case "receipt_or_application":
		var input ApplicationInput
		_ = json.Unmarshal(args.Input, &input)
		kind, id, project = "application_create", "", input.ProjectID
	case "receipt_or_rollback", "deployment_action":
		kind, id = "deployment", args.DeploymentID
	case "receipt_or_service_provision":
		kind, id = "service_provision_request", args.TemplateID
	case "service_provision_run":
		kind, id = "service_provision_run", args.RunID
	case "receipt_or_backup_policy":
		kind, id = "workload_backup_policy_create", ""
	case "backup_policy":
		kind, id = "workload_backup_policy", args.PolicyID
	case "infrastructure_action", "managed_server", "server_enrollment":
		kind, id, operation = "managed_server", args.ServerID, args.OperationID
	}
	out.Continuation = &Continuation{Kind: kind, ID: id, OperationID: operation, ProjectID: project, Key: args.Key}
	if !out.OK {
		if op.Mutation && args.Key == "" && out.Error != nil {
			switch out.Error.Code {
			case "timeout", "cancelled", "unavailable", "invalid_response", "response_limit":
				out.Error.Title = "The response is incomplete. Inspect the original resource and operation before submitting another request. No automatic retry occurred."
			}
		}
		return out
	}
	if op.Response == "receipt_or_application" || op.Response == "receipt_or_rollback" || op.Response == "receipt_or_service_provision" || op.Response == "receipt_or_backup_policy" {
		var result struct {
			ID            string `json:"id"`
			OperationID   string `json:"operationId"`
			ResourceID    string `json:"resourceId"`
			OperationKind string `json:"operationKind"`
		}
		expected := "application"
		if op.Response == "receipt_or_rollback" {
			expected = "deployment"
		}
		if op.Response == "receipt_or_service_provision" {
			expected = "service_provision"
		}
		if op.Response == "receipt_or_backup_policy" {
			expected = "workload_backup_policy"
		}
		if json.Unmarshal(out.Data, &result) != nil || !identifier.MatchString(result.ID) || !identifier.MatchString(result.OperationID) || !identifier.MatchString(result.ResourceID) || result.OperationKind != expected || out.Location != "/api/v1/mutation-receipts/"+result.ID {
			out.OK = false
			out.Error = &Problem{Code: "invalid_response", Title: "The controller did not return the expected durable receipt. Retry only the original request and key."}
			return out
		}
		out.Continuation = &Continuation{Kind: "receipt", ID: result.ID, OperationID: result.OperationID, ResourceID: result.ResourceID, ProjectID: project, Key: args.Key}
	}
	if op.Response == "backup_policy" {
		var result struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(out.Data, &result) != nil || result.ID != args.PolicyID {
			out.OK = false
			out.Error = &Problem{Code: "invalid_response", Title: "The response does not identify the original backup policy. Inspect that policy before submitting another request."}
		}
	}
	if op.Response == "service_provision_run" {
		var result struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(out.Data, &result) != nil || result.ID != args.RunID {
			out.OK = false
			out.Error = &Problem{Code: "invalid_response", Title: "The response does not identify the original service provision run."}
		}
	}
	if op.Response == "managed_server" {
		var result struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(out.Data, &result) != nil || result.ID != args.ServerID {
			out.OK = false
			out.Error = &Problem{Code: "invalid_response", Title: "The adoption response does not identify the original server. Inspect that server before submitting another request."}
		}
	}
	if op.Response == "server_enrollment" {
		var result struct {
			NodeID    string `json:"nodeId"`
			Token     string `json:"token"`
			ExpiresAt string `json:"expiresAt"`
		}
		decodeErr := json.Unmarshal(out.Data, &result)
		_, expiryErr := time.Parse(time.RFC3339, result.ExpiresAt)
		if decodeErr != nil || !identifier.MatchString(result.NodeID) || result.Token == "" || expiryErr != nil {
			out.OK = false
			out.Error = &Problem{Code: "invalid_response", Title: "Enrollment issuance did not return a complete credential. Inspect the original server before deliberately issuing another token."}
		}
	}
	return out
}

// Bind the operator's server selection to the operation before requesting a
// recovery action. The mutation itself still checks current server-side grants.
func (c *Client) checkInfrastructureAction(ctx context.Context, op Operation, args Arguments) *Result {
	if op.Response != "infrastructure_action" {
		return nil
	}
	out := c.request(ctx, "GET", "/infrastructure/servers/"+url.PathEscape(args.ServerID)+"/operations", "", nil)
	if !out.OK {
		out = workflowContinuation(op, args, out)
		return &out
	}
	var items []struct {
		ID       string `json:"id"`
		ServerID string `json:"serverId"`
	}
	if json.Unmarshal(out.Data, &items) != nil {
		out = workflowContinuation(op, args, Failure("invalid_response", "The controller did not return infrastructure operation history. No recovery action was submitted."))
		return &out
	}
	for _, item := range items {
		if item.ID == args.OperationID && item.ServerID == args.ServerID {
			return nil
		}
	}
	out = workflowContinuation(op, args, Failure("invalid_input", "The selected operation is not in this server's history. No recovery action was submitted."))
	return &out
}
