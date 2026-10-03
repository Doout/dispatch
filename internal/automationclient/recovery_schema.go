package automationclient

func recoverySchema(kind string) map[string]any {
	text := func() map[string]any { return map[string]any{"type": "string", "minLength": 1} }
	integer := func(min, max int) map[string]any {
		return map[string]any{"type": "integer", "minimum": min, "maximum": max}
	}
	object := func(props map[string]any, required []string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	switch kind {
	case "WorkloadBackupExportInput":
		return object(map[string]any{"storeId": text()}, []string{"storeId"})
	case "WorkloadBackupVerificationInput":
		return object(map[string]any{"destinationRunId": text()}, []string{"destinationRunId"})
	case "RecoveryConfirmation":
		confirmation := object(map[string]any{"resourceId": text(), "action": map[string]any{"type": "string", "enum": []string{"delete", "restore"}}, "expectedVersion": text(), "confirmName": text()}, []string{"resourceId", "action", "expectedVersion", "confirmName"})
		return object(map[string]any{"confirmation": confirmation}, []string{"confirmation"})
	case "RuntimeRetentionReviewInput", "RuntimeRetentionApplyInput":
		policy := object(map[string]any{"projectId": text(), "logDays": integer(1, 36500), "runDays": integer(1, 36500), "keepRuns": integer(5, 10000), "imageDays": integer(0, 36500), "stoppedRevisionDays": integer(0, 36500), "keepRollbackRevisions": map[string]any{"anyOf": []any{map[string]any{"type": "integer", "const": 0}, integer(2, 10000)}}}, []string{"projectId", "logDays", "runDays", "keepRuns", "imageDays", "stoppedRevisionDays", "keepRollbackRevisions"})
		props := map[string]any{"scope": map[string]any{"type": "string", "const": "runtime"}, "expectedPolicy": policy}
		required := []string{"scope", "expectedPolicy"}
		if kind == "RuntimeRetentionApplyInput" {
			for _, key := range []string{"confirm", "runtimeReviewId", "runtimeReviewDigest"} {
				props[key] = text()
				required = append(required, key)
			}
		}
		return object(props, required)
	case "WorkloadBackupCreateInput":
		check := object(map[string]any{"query": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096}, "expected": map[string]any{"type": "string", "maxLength": 4096}}, []string{"query", "expected"})
		return object(map[string]any{"sourceRunId": text(), "verificationIntervalHours": integer(0, 8760), "checks": map[string]any{"type": "array", "maxItems": 16, "items": check}}, []string{"sourceRunId"})
	}
	return nil
}
