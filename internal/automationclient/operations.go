package automationclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type DeploymentStart struct {
	CommitSHA string                 `json:"commitSha"`
	Review    *core.DeploymentReview `json:"review"`
}
type InfrastructureAcceptance struct {
	ReviewID    string `json:"reviewId,omitempty"`
	Digest      string `json:"digest"`
	ConfirmName string `json:"confirmName"`
}
type BootstrapInput struct {
	Method         string `json:"method"`
	Platform       string `json:"platform"`
	ImageFamily    string `json:"imageFamily"`
	InstallRuntime bool   `json:"installRuntime"`
}
type ProviderOptionsInput struct {
	Kind   string         `json:"kind"`
	Config map[string]any `json:"config"`
}
type SnapshotReviewInput struct {
	Name        string `json:"name"`
	DiskSet     string `json:"diskSet"`
	Consistency string `json:"consistency"`
	Encryption  struct {
		Mode string `json:"mode"`
	} `json:"encryption"`
	RetainUntil string `json:"retainUntil,omitempty"`
}
type ServerCreateReview struct {
	ProjectID        string          `json:"projectId"`
	ProviderID       string          `json:"providerId"`
	Name             string          `json:"name"`
	Region           string          `json:"region"`
	Size             string          `json:"size"`
	Image            string          `json:"image"`
	Network          string          `json:"network"`
	SSHKeySecretID   string          `json:"sshKeySecretId"`
	Config           map[string]any  `json:"config"`
	Bootstrap        *BootstrapInput `json:"bootstrap,omitempty"`
	SourceSnapshotID string          `json:"sourceSnapshotId,omitempty"`
}
type Arguments struct {
	ProjectID      string          `json:"projectId,omitempty"`
	ProviderID     string          `json:"providerId,omitempty"`
	SnapshotID     string          `json:"snapshotId,omitempty"`
	AppID          string          `json:"appId,omitempty"`
	DeploymentID   string          `json:"deploymentId,omitempty"`
	ReceiptID      string          `json:"receiptId,omitempty"`
	ServerID       string          `json:"serverId,omitempty"`
	Revision       string          `json:"revision,omitempty"`
	Key            string          `json:"key,omitempty"`
	Input          json.RawMessage `json:"input,omitempty"`
	Limit          int             `json:"limit,omitempty"`
	After          int64           `json:"after,omitempty"`
	TimeoutSeconds int             `json:"timeoutSeconds,omitempty"`
}
type Operation struct {
	Name, Method, Path, Description string
	Fields, Required                []string
	Mutation                        bool
	InputSchema                     string
}

var Operations = []Operation{
	{Name: "projects_list", Method: "GET", Path: "/projects", Description: "List projects visible to the current scoped identity."},
	{Name: "apps_list", Method: "GET", Path: "/apps", Description: "List visible applications, optionally filtered by project.", Fields: []string{"projectId"}},
	{Name: "deployment_preview", Method: "POST", Path: "/apps/{appId}/release-preview", Description: "Inspect release inputs and obtain the server's point-in-time deployment review.", Fields: []string{"appId", "revision"}, Required: []string{"appId"}},
	{Name: "deployment_start", Method: "POST", Path: "/apps/{appId}/deployments", Description: "Submit a supplied deployment review with a caller-chosen retry key. Server approval and trust requirements still apply.", Fields: []string{"appId", "key", "input"}, Required: []string{"appId", "key", "input"}, Mutation: true, InputSchema: "DeploymentStart"},
	{Name: "deployment_get", Method: "GET", Path: "/deployments/{deploymentId}", Description: "Inspect deployment state and evidence.", Fields: []string{"deploymentId"}, Required: []string{"deploymentId"}},
	{Name: "deployment_logs", Method: "GET", Path: "/deployments/{deploymentId}/logs", Description: "Get at most 500 log entries after a saved log cursor.", Fields: []string{"deploymentId", "limit", "after"}, Required: []string{"deploymentId"}},
	{Name: "deployment_diagnose", Method: "GET", Path: "/deployments/{deploymentId}/diagnosis", Description: "Get sanitized diagnosis and next steps for a deployment.", Fields: []string{"deploymentId"}, Required: []string{"deploymentId"}},
	{Name: "deployment_wait", Method: "GET", Path: "/deployments/{deploymentId}", Description: "Wait up to a bounded deadline for a deployment, preserving its ID if interrupted. Does not cancel server work.", Fields: []string{"deploymentId", "timeoutSeconds"}, Required: []string{"deploymentId"}},
	{Name: "receipt_get", Method: "GET", Path: "/mutation-receipts/{receiptId}", Description: "Inspect the original mutation receipt using current permissions.", Fields: []string{"receiptId"}, Required: []string{"receiptId"}},
	{Name: "receipt_wait", Method: "GET", Path: "/mutation-receipts/{receiptId}", Description: "Wait for a receipt outcome. Pending approval and uncertain outcomes return for operator review.", Fields: []string{"receiptId", "timeoutSeconds"}, Required: []string{"receiptId"}},
	{Name: "servers_list", Method: "GET", Path: "/infrastructure/servers", Description: "List managed servers visible to the current identity.", Fields: []string{"projectId"}},
	{Name: "providers_list", Method: "GET", Path: "/projects/{projectId}/infrastructure/providers", Description: "List assigned providers, their capabilities and supported configuration for a project.", Fields: []string{"projectId"}, Required: []string{"projectId"}},
	{Name: "provider_options", Method: "POST", Path: "/projects/{projectId}/infrastructure/providers/{providerId}/options", Description: "Inspect available provider regions, sizes, images or networks using public configuration.", Fields: []string{"projectId", "providerId", "input"}, Required: []string{"projectId", "providerId", "input"}, InputSchema: "ProviderOptionsInput"},
	{Name: "server_operations", Method: "GET", Path: "/infrastructure/servers/{serverId}/operations", Description: "Inspect original server operation history and recovery state.", Fields: []string{"serverId"}, Required: []string{"serverId"}},
	{Name: "server_review", Method: "POST", Path: "/infrastructure/servers/review", Description: "Prepare a server creation review using assigned provider and public SSH key references.", Fields: []string{"input"}, Required: []string{"input"}, InputSchema: "ServerCreateReview"},
	{Name: "server_create", Method: "POST", Path: "/infrastructure/servers", Description: "Accept a supplied server creation review with an explicit key. Project grants, assignments and quota are enforced by Dispatch.", Fields: []string{"key", "input"}, Required: []string{"key", "input"}, Mutation: true, InputSchema: "InfrastructureAcceptance"},
	{Name: "server_delete_review", Method: "POST", Path: "/infrastructure/servers/{serverId}/delete-review", Description: "Preview server deletion and protected data consequences without accepting deletion.", Fields: []string{"serverId"}, Required: []string{"serverId"}},
	{Name: "server_delete", Method: "POST", Path: "/infrastructure/servers/{serverId}/delete", Description: "Submit the operator-supplied deletion digest and exact name with an explicit key. Never generates confirmation or bypasses protected storage.", Fields: []string{"serverId", "key", "input"}, Required: []string{"serverId", "key", "input"}, Mutation: true, InputSchema: "InfrastructureAcceptance"},
	{Name: "snapshots_list", Method: "GET", Path: "/projects/{projectId}/infrastructure/snapshots", Description: "List owned machine snapshots and their retention and recovery state.", Fields: []string{"projectId"}, Required: []string{"projectId"}},
	{Name: "snapshot_get", Method: "GET", Path: "/infrastructure/snapshots/{snapshotId}", Description: "Inspect machine snapshot state and source identity.", Fields: []string{"snapshotId"}, Required: []string{"snapshotId"}},
	{Name: "snapshot_review", Method: "POST", Path: "/infrastructure/servers/{serverId}/snapshot-review", Description: "Prepare a machine snapshot capture review with explicit disk scope, consistency and encryption.", Fields: []string{"serverId", "input"}, Required: []string{"serverId", "input"}, InputSchema: "SnapshotReviewInput"},
	{Name: "snapshot_delete_review", Method: "POST", Path: "/infrastructure/snapshots/{snapshotId}/delete-review", Description: "Review snapshot deletion, including retention and active restore protection.", Fields: []string{"snapshotId"}, Required: []string{"snapshotId"}},
	{Name: "snapshot_accept", Method: "POST", Path: "/infrastructure/snapshots/accept", Description: "Accept a supplied snapshot capture or deletion review with an explicit retry key and confirmation. Server grants, quota and retention still apply.", Fields: []string{"key", "input"}, Required: []string{"key", "input"}, Mutation: true, InputSchema: "InfrastructureAcceptance"},
	{Name: "quota_get", Method: "GET", Path: "/projects/{projectId}/infrastructure/quota", Description: "Inspect project allocation limits and reservations.", Fields: []string{"projectId"}, Required: []string{"projectId"}},
}
var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,255}$`)

func DecodeArguments(raw []byte) (Arguments, error) {
	var a Arguments
	err := decodeStrict(raw, &a)
	return a, err
}
func decodeStrict(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}
func Find(name string) (Operation, bool) {
	for _, op := range Operations {
		if op.Name == name {
			return op, true
		}
	}
	return Operation{}, false
}
func (c *Client) Call(ctx context.Context, name string, args Arguments) Result {
	result := c.call(ctx, name, args)
	raw, _ := json.Marshal(result)
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&value)
	raw, _ = json.Marshal(redact(value, c.token))
	_ = json.Unmarshal(raw, &result)
	return result
}

func (c *Client) call(ctx context.Context, name string, args Arguments) Result {
	op, ok := Find(name)
	if !ok {
		return Failure("invalid_input", "Unknown operation")
	}
	raw, _ := json.Marshal(args)
	var values map[string]json.RawMessage
	_ = json.Unmarshal(raw, &values)
	allowed := map[string]bool{}
	for _, f := range op.Fields {
		allowed[f] = true
	}
	for f := range values {
		if !allowed[f] {
			return Failure("invalid_input", "Argument is not supported by this operation: "+f)
		}
	}
	for _, f := range op.Required {
		if len(values[f]) == 0 {
			return Failure("invalid_input", "Missing required argument: "+f)
		}
	}
	path := op.Path
	for f, v := range map[string]string{"projectId": args.ProjectID, "providerId": args.ProviderID, "snapshotId": args.SnapshotID, "appId": args.AppID, "deploymentId": args.DeploymentID, "receiptId": args.ReceiptID, "serverId": args.ServerID} {
		if v != "" && !identifier.MatchString(v) {
			return Failure("invalid_input", "Invalid resource identifier")
		}
		path = strings.ReplaceAll(path, "{"+f+"}", url.PathEscape(v))
	}
	if args.Key != "" && (len(args.Key) < 8 || len(args.Key) > 128 || strings.ContainsFunc(args.Key, func(r rune) bool { return r < 33 || r > 126 })) {
		return Failure("invalid_input", "Use a stable retry key of 8 to 128 printable ASCII characters without spaces")
	}
	var body any
	switch op.InputSchema {
	case "DeploymentStart":
		var input DeploymentStart
		if decodeStrict(args.Input, &input) != nil || strings.TrimSpace(input.CommitSHA) == "" || input.Review == nil || input.Review.ExpectedAppName == "" || input.Review.ProjectID == "" || input.Review.AppSpecDigest == "" || input.Review.BindingsDigest == "" || input.Review.ServiceRevisions == nil {
			return Failure("invalid_input", "Supply commitSha and the complete review returned by deployment_preview")
		}
		body = input
	case "InfrastructureAcceptance":
		var input InfrastructureAcceptance
		if decodeStrict(args.Input, &input) != nil || input.Digest == "" || input.ConfirmName == "" || (name == "server_create" || name == "snapshot_accept") && input.ReviewID == "" {
			return Failure("invalid_input", "Supply the reviewed digest, exact confirmation name and creation review ID when creating")
		}
		body = input
	case "ProviderOptionsInput":
		var input ProviderOptionsInput
		if decodeStrict(args.Input, &input) != nil || (input.Kind != "regions" && input.Kind != "sizes" && input.Kind != "images" && input.Kind != "networks") {
			return Failure("invalid_input", "Supply an option kind of regions, sizes, images or networks and public configuration")
		}
		body = input
	case "SnapshotReviewInput":
		var input SnapshotReviewInput
		if decodeStrict(args.Input, &input) != nil || input.Name == "" || (input.DiskSet != "boot" && input.DiskSet != "all") || input.Consistency != "crash-consistent" || input.Encryption.Mode != "provider-managed" {
			return Failure("invalid_input", "Supply a snapshot name, boot or all disk scope, crash-consistent policy and provider-managed encryption")
		}
		if input.RetainUntil != "" {
			if _, err := time.Parse(time.RFC3339, input.RetainUntil); err != nil {
				return Failure("invalid_input", "Snapshot retention must be an RFC3339 timestamp")
			}
		}
		body = input
	case "ServerCreateReview":
		var input ServerCreateReview
		if decodeStrict(args.Input, &input) != nil || input.ProjectID == "" || input.ProviderID == "" || input.Name == "" {
			return Failure("invalid_input", "Supply the project, assigned provider and typed server creation fields")
		}
		body = input
	default:
		if op.Method == "POST" {
			body = map[string]any{}
		}
	}
	if name == "deployment_preview" {
		body = map[string]string{"revision": args.Revision}
	}
	if args.Limit < 0 || args.Limit > 500 || args.After < 0 {
		return Failure("invalid_input", "Log limit must be 1 to 500 and the cursor must not be negative")
	}
	if name == "deployment_logs" {
		if args.Limit == 0 {
			args.Limit = 100
		}
		path += "?after=" + strconv.FormatInt(args.After, 10) + "&limit=" + strconv.Itoa(args.Limit)
	}
	if args.TimeoutSeconds < 0 || args.TimeoutSeconds > 600 {
		return Failure("invalid_input", "Wait timeout must be 1 to 600 seconds")
	}
	continuation := &Continuation{Kind: "deployment", ID: args.DeploymentID, Key: args.Key}
	if args.ReceiptID != "" {
		continuation.Kind = "receipt"
		continuation.ID = args.ReceiptID
	}
	wait := strings.HasSuffix(name, "_wait")
	if wait {
		timeout := args.TimeoutSeconds
		if timeout == 0 {
			timeout = 120
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
	}
	for {
		out := c.request(ctx, op.Method, path, args.Key, body)
		if op.Mutation {
			out.Continuation = &Continuation{Kind: name, ID: args.AppID + args.ServerID, Key: args.Key}
		}
		if wait {
			out.Continuation = continuation
		}
		if op.Mutation && out.OK {
			var accepted struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(out.Data, &accepted) == nil && accepted.ID != "" {
				out.Continuation = &Continuation{Kind: "receipt", ID: accepted.ID, Key: args.Key}
			}
		}

		if !out.OK {
			return out
		}
		if (name == "apps_list" || name == "servers_list") && args.ProjectID != "" {
			var items []map[string]any
			decoder := json.NewDecoder(bytes.NewReader(out.Data))
			decoder.UseNumber()
			if decoder.Decode(&items) != nil {
				return Failure("invalid_response", "Expected resource inventory")
			}
			selected := []map[string]any{}
			for _, item := range items {
				if item["projectId"] == args.ProjectID {
					selected = append(selected, item)
				}
			}
			out.Data, _ = json.Marshal(selected)
		}
		if name == "deployment_logs" {
			var items []json.RawMessage
			if json.Unmarshal(out.Data, &items) != nil {
				return Failure("invalid_response", "Expected bounded log entries")
			}
			if len(items) > args.Limit {
				items = items[:args.Limit]
			}
			out.Data, _ = json.Marshal(items)
		}
		if !wait {
			return out
		}
		var state struct {
			State string `json:"state"`
		}
		if json.Unmarshal(out.Data, &state) != nil || state.State == "" {
			return Failure("invalid_response", "Operation response has no state")
		}
		switch state.State {
		case "succeeded":
			return out
		case "failed", "cancelled", "canceled", "unknown", "unresolved":
			out.OK = false
			code := "operation_failed"
			if state.State == "cancelled" || state.State == "canceled" {
				code = "operation_cancelled"
			}
			if state.State == "unknown" || state.State == "unresolved" {
				code = "outcome_unknown"
			}
			out.Error = &Problem{Code: code, Title: "Inspect the original operation and its recovery actions"}
			return out
		case "pending_approval", "waiting_approval", "paused":
			return out
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			out.OK = false
			code := "timeout"
			if ctx.Err() == context.Canceled {
				code = "cancelled"
			}
			out.Error = &Problem{Code: code, Title: "Client stopped waiting. Resume with the saved ID; accepted work was not cancelled."}
			return out
		case <-timer.C:
		}
	}
}
