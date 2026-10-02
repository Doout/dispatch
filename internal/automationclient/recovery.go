package automationclient

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

type ResourceConfirmation struct {
	ResourceID      string `json:"resourceId"`
	Action          string `json:"action"`
	ExpectedVersion string `json:"expectedVersion"`
	ConfirmName     string `json:"confirmName"`
}
type RecoveryConfirmation struct {
	Confirmation ResourceConfirmation `json:"confirmation"`
}
type RuntimeRetentionReviewInput struct {
	Scope          string                `json:"scope"`
	ExpectedPolicy *core.RetentionPolicy `json:"expectedPolicy"`
}
type RuntimeRetentionApplyInput struct {
	Scope               string                `json:"scope"`
	ExpectedPolicy      *core.RetentionPolicy `json:"expectedPolicy"`
	Confirm             string                `json:"confirm"`
	RuntimeReviewID     string                `json:"runtimeReviewId"`
	RuntimeReviewDigest string                `json:"runtimeReviewDigest"`
}
type WorkloadBackupCreateInput struct {
	SourceRunID               string                      `json:"sourceRunId"`
	VerificationIntervalHours int                         `json:"verificationIntervalHours"`
	Checks                    []core.BackupIntegrityCheck `json:"checks,omitempty"`
}

var recoveryOperations = []Operation{
	{Name: "service_get", Method: "GET", Path: "/service-provision-runs/{runId}/resource", Description: "Inspect the original owned service resource, operation ID and recovery timing.", Fields: []string{"runId"}, Required: []string{"runId"}, Response: "service_resource"},
	{Name: "service_inspect", Method: "POST", Path: "/service-provision-runs/{runId}/resource/inspect", Description: "Inspect current ownership and readiness of the same service resource without provisioning or executing scripts.", Fields: []string{"runId"}, Required: []string{"runId"}, Response: "service_resource"},
	{Name: "service_reconcile", Method: "POST", Path: "/service-provision-runs/{runId}/resource/reconcile", Description: "Explicitly recover the original service binding or cleanup after inspecting its operation. Active leases and ownership conflicts remain blocking. Inspect again if the response is lost.", Fields: []string{"runId"}, Required: []string{"runId"}, Mutation: true, NonIdempotent: true, Destructive: true, Response: "service_resource"},
	{Name: "service_retry", Method: "POST", Path: "/service-provision-runs/{runId}/resource/retry", Description: "Explicitly retry the accepted built-in service operation after inspection. May recreate a confirmed-absent resource with its original credentials or resume original cleanup; never invents a new provision run.", Fields: []string{"runId"}, Required: []string{"runId"}, Mutation: true, NonIdempotent: true, Destructive: true, Response: "service_resource"},
	{Name: "service_delete_review", Method: "POST", Path: "/service-provision-runs/{runId}/resource/delete-preview", Description: "Review owned service deletion, consumers and protected storage. Does not accept deletion.", Fields: []string{"runId"}, Required: []string{"runId"}},
	{Name: "service_delete", Method: "POST", Path: "/service-provision-runs/{runId}/resource/delete", Description: "Accept caller-supplied service deletion confirmation with its current version and a stable retry key. Retains protected storage and history.", Fields: []string{"runId", "key", "input"}, Required: []string{"runId", "key", "input"}, Mutation: true, Destructive: true, InputSchema: "RecoveryConfirmation", Response: "receipt_or_service_resource"},
	{Name: "retention_policy", Method: "GET", Path: "/projects/{projectId}/retention", Description: "Read the saved retention policy. Requires project.manage and Operations enabled; does not change policy.", Fields: []string{"projectId"}, Required: []string{"projectId"}},
	{Name: "retention_review", Method: "POST", Path: "/projects/{projectId}/retention/preview", Description: "Create a runtime artifact review using the unchanged saved policy. Returns exact candidates and protection reasons; no storage or backups are deleted.", Fields: []string{"projectId", "input"}, Required: []string{"projectId", "input"}, CreatesReview: true, InputSchema: "RuntimeRetentionReviewInput", Response: "retention_result"},
	{Name: "retention_apply", Method: "POST", Path: "/projects/{projectId}/retention/apply", Description: "Apply the supplied runtime review ID, digest, saved policy and explicit project confirmation. Partial retries remain within the same candidate set; inspect the original review after interruption.", Fields: []string{"projectId", "input"}, Required: []string{"projectId", "input"}, Mutation: true, Destructive: true, InputSchema: "RuntimeRetentionApplyInput", Response: "retention_result"},
	{Name: "retention_get", Method: "GET", Path: "/projects/{projectId}/retention/runtime-reviews/{reviewId}", Description: "Inspect the original runtime retention review, per-item outcomes and any superseding review without scheduling cleanup.", Fields: []string{"projectId", "reviewId"}, Required: []string{"projectId", "reviewId"}, Response: "retention_review"},
	{Name: "backups_list", Method: "GET", Path: "/workload-backups", Description: "List visible native workload backups, optionally scoped to a project. Controller backups and machine snapshots are separate.", Fields: []string{"projectId"}},
	{Name: "backup_create", Method: "POST", Path: "/workload-backups", Description: "Capture a native encrypted backup of an owned PostgreSQL 17+ Docker service with a stable retry key. Archives retain on their target and protect target deletion.", Fields: []string{"key", "input"}, Required: []string{"key", "input"}, Mutation: true, InputSchema: "WorkloadBackupCreateInput", Response: "receipt_or_backup_operation"},
	{Name: "backup_get", Method: "GET", Path: "/workload-backups/{backupId}", Description: "Inspect retained backup metadata and verification freshness without retrieving archive keys or credentials.", Fields: []string{"backupId"}, Required: []string{"backupId"}, Response: "workload_backup"},
	{Name: "backup_operations", Method: "GET", Path: "/workload-backups/{backupId}/operations", Description: "List original backup operations, cleanup evidence and recovery timing.", Fields: []string{"backupId"}, Required: []string{"backupId"}},
	{Name: "backup_operation_get", Method: "GET", Path: "/workload-backup-operations/{operationId}", Description: "Inspect a native backup operation without repeating backup, verification or restore.", Fields: []string{"operationId"}, Required: []string{"operationId"}, Response: "backup_operation"},
	{Name: "backup_verify", Method: "POST", Path: "/workload-backups/{backupId}/verify", Description: "Accept isolated restore verification of a retained backup with a stable retry key. Verification requires integrity checks and cleanup; source data is unchanged.", Fields: []string{"backupId", "key"}, Required: []string{"backupId", "key"}, Mutation: true, Response: "receipt_or_backup_operation"},
	{Name: "backup_reconcile", Method: "POST", Path: "/workload-backups/{backupId}/operations/{operationId}/reconcile", Description: "Inspect original operation evidence after its recovery deadline and clean owned verification resources. Never replays an unknown restore. Inspect the original operation if interrupted.", Fields: []string{"backupId", "operationId"}, Required: []string{"backupId", "operationId"}, Mutation: true, NonIdempotent: true, Destructive: true, Response: "backup_operation"},
	{Name: "backup_delete_review", Method: "POST", Path: "/workload-backups/{backupId}/delete-preview", Description: "Review permanent archive-byte deletion and current operation blockers. Does not accept deletion.", Fields: []string{"backupId"}, Required: []string{"backupId"}},
	{Name: "backup_delete", Method: "POST", Path: "/workload-backups/{backupId}/delete", Description: "Accept the supplied backup deletion confirmation with a stable retry key. Retained encrypted history remains.", Fields: []string{"backupId", "key", "input"}, Required: []string{"backupId", "key", "input"}, Mutation: true, Destructive: true, InputSchema: "RecoveryConfirmation", Response: "receipt_or_backup_operation"},
	{Name: "backup_restore_review", Method: "POST", Path: "/workload-backups/{backupId}/restore/{destinationId}/preview", Description: "Review overwrite of an explicitly selected owned destination service on the same project and target. Active consumers block restore.", Fields: []string{"backupId", "destinationId"}, Required: []string{"backupId", "destinationId"}},
	{Name: "backup_restore", Method: "POST", Path: "/workload-backups/{backupId}/restore/{destinationId}", Description: "Submit the exact reviewed destination confirmation and stable restore key. An unknown outcome requires original-operation inspection; no automatic restore retry occurs.", Fields: []string{"backupId", "destinationId", "key", "input"}, Required: []string{"backupId", "destinationId", "key", "input"}, Mutation: true, Destructive: true, InputSchema: "RecoveryConfirmation", Response: "receipt_or_backup_operation"},
}

func recoveryInput(op Operation, args Arguments) (any, error) {
	fail := func(message string) (any, error) { return nil, errors.New(message) }
	switch op.InputSchema {
	case "RecoveryConfirmation":
		var v RecoveryConfirmation
		if decodeStrict(args.Input, &v) != nil {
			return fail("Supply the complete reviewed confirmation object")
		}
		resource, action := args.RunID, "delete"
		if args.BackupID != "" {
			resource = args.BackupID
		}
		if op.Name == "backup_restore" {
			action = "restore"
		}
		if v.Confirmation.ResourceID != resource || v.Confirmation.Action != action || v.Confirmation.ExpectedVersion == "" || v.Confirmation.ConfirmName == "" {
			return fail("Supply the exact reviewed resource ID, action, version and confirmation name for this operation")
		}
		return v, nil
	case "RuntimeRetentionReviewInput":
		var v RuntimeRetentionReviewInput
		if decodeStrict(args.Input, &v) != nil || v.Scope != "runtime" || !completeRetentionPolicy(args.Input, args.ProjectID, v.ExpectedPolicy) {
			return fail("Supply scope runtime and the complete unchanged saved policy for the selected project")
		}
		return v, nil
	case "RuntimeRetentionApplyInput":
		var v RuntimeRetentionApplyInput
		if decodeStrict(args.Input, &v) != nil || v.Scope != "runtime" || !completeRetentionPolicy(args.Input, args.ProjectID, v.ExpectedPolicy) || v.Confirm != args.ProjectID || !identifier.MatchString(v.RuntimeReviewID) || v.RuntimeReviewDigest == "" {
			return fail("Supply the original runtime review ID, digest, unchanged saved policy and explicit project confirmation")
		}
		return v, nil
	case "WorkloadBackupCreateInput":
		var v WorkloadBackupCreateInput
		if decodeStrict(args.Input, &v) != nil || !identifier.MatchString(v.SourceRunID) || v.VerificationIntervalHours < 0 || v.VerificationIntervalHours > 8760 || len(v.Checks) > 16 {
			return fail("Supply an owned source run, a verification interval from 0 to 8760 hours, and at most 16 integrity checks")
		}
		var raw struct {
			Checks []map[string]json.RawMessage `json:"checks"`
		}
		if json.Unmarshal(args.Input, &raw) != nil {
			return fail("Supply explicit integrity assertions")
		}
		for _, check := range raw.Checks {
			expected, exists := check["expected"]
			if !exists || string(expected) == "null" {
				return fail("Each integrity assertion must supply its expected value explicitly")
			}
		}
		for _, check := range v.Checks {
			if strings.TrimSpace(check.Query) == "" || len(check.Query) > 4096 || len(check.Expected) > 4096 {
				return fail("Integrity checks require a query and expected value of at most 4096 bytes each; the server validates read-only SQL")
			}
		}
		return v, nil
	}
	return nil, nil
}
func completeRetentionPolicy(raw json.RawMessage, project string, p *core.RetentionPolicy) bool {
	if p == nil || p.ProjectID != project {
		return false
	}
	var input struct {
		Policy map[string]json.RawMessage `json:"expectedPolicy"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return false
	}
	for _, field := range []string{"projectId", "logDays", "runDays", "keepRuns", "imageDays", "stoppedRevisionDays", "keepRollbackRevisions"} {
		value, ok := input.Policy[field]
		if !ok || string(value) == "null" {
			return false
		}
	}
	return p.LogDays >= 1 && p.LogDays <= 36500 && p.RunDays >= 1 && p.RunDays <= 36500 && p.KeepRuns >= 5 && p.KeepRuns <= 10000 && p.ImageDays >= 0 && p.ImageDays <= 36500 && p.StoppedRevisionDays >= 0 && p.StoppedRevisionDays <= 36500 && (p.KeepRollbackRevisions == 0 || p.KeepRollbackRevisions >= 2 && p.KeepRollbackRevisions <= 10000)
}

func recoveryContinuation(op Operation, args Arguments, out Result) Result {
	if op.Response == "" {
		return out
	}
	kind, id, resource, operation, project := "", "", "", "", args.ProjectID
	switch op.Response {
	case "service_resource", "receipt_or_service_resource":
		kind, id = "service_resource", args.RunID
	case "retention_result", "retention_review":
		kind, id = "runtime_retention_review", args.ReviewID
		if op.Name == "retention_apply" {
			var input RuntimeRetentionApplyInput
			_ = json.Unmarshal(args.Input, &input)
			id = input.RuntimeReviewID
		}
	case "workload_backup":
		kind, id = "workload_backup", args.BackupID
	case "backup_operation", "receipt_or_backup_operation":
		kind, id, resource = "workload_backup_operation", args.OperationID, args.BackupID
	}
	if !out.OK {
		if id != "" {
			out.Continuation = &Continuation{Kind: kind, ID: id, ResourceID: resource, ProjectID: project, Key: args.Key}
		} else if args.Key != "" {
			out.Continuation = &Continuation{Kind: op.Name, Key: args.Key}
		}
		if op.Mutation && args.Key == "" && out.Error != nil {
			switch out.Error.Code {
			case "timeout", "cancelled", "unavailable", "invalid_response", "response_limit":
				out.Error.Title = "The response is incomplete. Inspect the original resource, operation or runtime review before submitting another request. No automatic retry occurred."
			}
		}
		return out
	}
	var result struct {
		ID            string `json:"id"`
		RunID         string `json:"runId"`
		BackupID      string `json:"backupId"`
		OperationID   string `json:"operationId"`
		OperationKind string `json:"operationKind"`
		ResourceID    string `json:"resourceId"`
		ProjectID     string `json:"projectId"`
		Runtime       *struct {
			ID        string `json:"id"`
			ProjectID string `json:"projectId"`
		} `json:"runtime"`
	}
	invalid := func() Result {
		out.OK = false
		out.Error = &Problem{Code: "invalid_response", Title: "The controller response does not identify the expected recovery record. Inspect the original request before submitting another mutation."}
		return out
	}
	if json.Unmarshal(out.Data, &result) != nil {
		return invalid()
	}
	if result.ProjectID != "" {
		project = result.ProjectID
	}
	switch op.Response {
	case "receipt_or_service_resource", "receipt_or_backup_operation":
		if result.OperationKind != "" || strings.HasPrefix(out.Location, "/api/v1/mutation-receipts/") {
			kind, id, resource, operation = "receipt", result.ID, result.ResourceID, result.OperationID
			if operation == "" {
				return invalid()
			}
		} else if op.Response == "receipt_or_service_resource" {
			kind, id, operation = "service_resource", result.RunID, result.OperationID
		} else {
			kind, id, resource = "workload_backup_operation", result.ID, result.BackupID
			if resource == "" {
				return invalid()
			}
		}
	case "service_resource":
		id, operation = result.RunID, result.OperationID
	case "backup_operation":
		id, resource = result.ID, result.BackupID
		if resource == "" {
			return invalid()
		}
	case "workload_backup", "retention_review":
		id = result.ID
	case "retention_result":
		if result.Runtime == nil {
			return invalid()
		}
		id, project = result.Runtime.ID, result.Runtime.ProjectID
	}
	if !identifier.MatchString(id) {
		return invalid()
	}
	if project != "" && !identifier.MatchString(project) || resource != "" && !identifier.MatchString(resource) || operation != "" && !identifier.MatchString(operation) {
		return invalid()
	}
	out.Continuation = &Continuation{Kind: kind, ID: id, ResourceID: resource, OperationID: operation, ProjectID: project, Key: args.Key}
	return out
}
