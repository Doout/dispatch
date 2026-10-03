package automationclient

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

type BackupPolicyCreateInput struct {
	RetireLocalAfterOffsiteVerification bool                        `json:"retireLocalAfterOffsiteVerification,omitempty"`
	OffsiteStoreID                      string                      `json:"offsiteStoreId,omitempty"`
	ConfirmOffsiteStoreID               string                      `json:"confirmOffsiteStoreId,omitempty"`
	OffsiteStaleAfterHours              int                         `json:"offsiteStaleAfterHours,omitempty"`
	NotificationAppID                   string                      `json:"notificationAppId,omitempty"`
	Name                                string                      `json:"name"`
	SourceRunID                         string                      `json:"sourceRunId"`
	IntervalHours                       int                         `json:"intervalHours"`
	KeepLast                            int                         `json:"keepLast"`
	ConfirmRetention                    string                      `json:"confirmRetention"`
	Checks                              []core.BackupIntegrityCheck `json:"checks,omitempty"`
}

type BackupPolicyUpdateInput struct {
	Revision    int64  `json:"revision"`
	Enabled     *bool  `json:"enabled"`
	ConfirmName string `json:"confirmName"`
}

var backupPolicyClientOperations = []Operation{
	{Name: "backup_policies_list", Method: "GET", Path: "/workload-backup-policies", Description: "List visible scheduled workload backup policies, capture and offsite verification freshness, missed captures or exports and blockers. Approved policies export encrypted captures before reporting offsite protection.", Fields: []string{"projectId"}},
	{Name: "backup_policy_get", Method: "GET", Path: "/workload-backup-policies/{policyId}", Description: "Inspect an existing backup policy's frozen source, cadence, retention, latest locally verified and offsite recovery points and current state without running a capture.", Fields: []string{"policyId"}, Required: []string{"policyId"}, Response: "backup_policy"},
	{Name: "backup_policy_create", Method: "POST", Path: "/workload-backup-policies", Description: "Create a scheduled PostgreSQL backup policy using a stable retry key. Explicitly confirm automatic removal of older verified policy archives with the exact policy name. Optionally approve an immutable same-project offsiteStoreId with exact confirmOffsiteStoreId for one export per captured archive, followed by offsite restore verification. retireLocalAfterOffsiteVerification explicitly approves bounded older local-byte retirement while preserving verified offsite objects and controller keys. notificationAppId opts into an existing enabled same-project observation webhook. Requires project.configure, deployment.run and an assigned owned source.", Fields: []string{"key", "input"}, Required: []string{"key", "input"}, Mutation: true, Destructive: true, InputSchema: "BackupPolicyCreateInput", Response: "receipt_or_backup_policy"},
	{Name: "backup_policy_set", Method: "PUT", Path: "/workload-backup-policies/{policyId}", Description: "Pause or resume the existing policy using its current revision, explicit enabled value and exact name. Resume rechecks the original actor and target. Inspect after response loss; cadence, source and retention stay unchanged.", Fields: []string{"policyId", "input"}, Required: []string{"policyId", "input"}, Mutation: true, NonIdempotent: true, Destructive: true, InputSchema: "BackupPolicyUpdateInput", Response: "backup_policy"},
}

func backupPolicyClientInput(op Operation, args Arguments) (any, error) {
	switch op.InputSchema {
	case "BackupPolicyCreateInput":
		var input BackupPolicyCreateInput
		if decodeStrict(args.Input, &input) != nil || len(strings.TrimSpace(input.Name)) < 2 || len(input.Name) > 80 || strings.ContainsAny(input.Name, "\x00\r\n") || !identifier.MatchString(input.SourceRunID) || input.IntervalHours < 1 || input.IntervalHours > 8760 || input.KeepLast < 1 || input.KeepLast > 1000 || input.ConfirmRetention != input.Name || len(input.Checks) > 16 {
			return nil, errors.New("Supply an owned source, cadence of 1 to 8760 hours, 1 to 1000 retained verified backups and exact policy-name retention confirmation")
		}
		if input.OffsiteStoreID == "" && (input.ConfirmOffsiteStoreID != "" || input.OffsiteStaleAfterHours != 0 || input.NotificationAppID != "" || input.RetireLocalAfterOffsiteVerification) || input.OffsiteStoreID != "" && (!identifier.MatchString(input.OffsiteStoreID) || input.ConfirmOffsiteStoreID != input.OffsiteStoreID) || input.OffsiteStaleAfterHours != 0 && (input.OffsiteStaleAfterHours < input.IntervalHours || input.OffsiteStaleAfterHours > 17520) || input.NotificationAppID != "" && !identifier.MatchString(input.NotificationAppID) {
			return nil, errors.New("Confirm the exact approved offsite destination ID; optional freshness must be at least the capture interval and at most 17520 hours, and notificationAppId explicitly uses an existing enabled project webhook")
		}
		var raw struct {
			Checks []map[string]json.RawMessage `json:"checks"`
		}
		if json.Unmarshal(args.Input, &raw) != nil {
			return nil, errors.New("Supply explicit backup integrity assertions")
		}
		for _, check := range raw.Checks {
			expected, exists := check["expected"]
			if !exists || string(expected) == "null" {
				return nil, errors.New("Each integrity assertion must supply its expected value explicitly")
			}
		}
		for _, check := range input.Checks {
			if strings.TrimSpace(check.Query) == "" || len(check.Query) > 4096 || len(check.Expected) > 4096 {
				return nil, errors.New("Supply bounded read-only integrity assertions with explicit expected values")
			}
		}
		return input, nil
	case "BackupPolicyUpdateInput":
		var input BackupPolicyUpdateInput
		if decodeStrict(args.Input, &input) != nil || input.Revision < 1 || input.Enabled == nil || strings.TrimSpace(input.ConfirmName) == "" {
			return nil, errors.New("Supply the current policy revision, explicit enabled state and exact policy name")
		}
		return input, nil
	}
	return nil, nil
}
