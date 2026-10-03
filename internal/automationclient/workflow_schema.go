package automationclient

func workflowSchema(kind string) map[string]any {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	object := func(props map[string]any, required []string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	health := func() map[string]any {
		props := map[string]any{}
		for key, max := range map[string]int{"timeoutSeconds": 600, "checkTimeoutSeconds": 60, "intervalSeconds": 60, "failureThreshold": 20} {
			props[key] = map[string]any{"type": "integer", "minimum": 0, "maximum": max}
		}
		check := object(map[string]any{"id": text(), "kind": map[string]any{"type": "string", "enum": []string{"container", "http", "tcp", "tls"}}, "scope": map[string]any{"type": "string", "enum": []string{"workload", "route", "certificate"}}, "service": text(), "port": map[string]any{"type": "integer", "minimum": 0, "maximum": 65535}, "path": text()}, []string{"id", "kind"})
		props["checks"] = map[string]any{"type": "array", "maxItems": 17, "items": check}
		return object(props, []string{})
	}
	switch kind {
	case "ServiceProvisionInput":
		return object(map[string]any{"name": text(), "description": text(), "inputs": map[string]any{"type": "object", "additionalProperties": text()}}, []string{"name", "inputs"})
	case "ApplicationInput":
		props := map[string]any{}
		for _, key := range []string{"projectId", "serverId", "name", "sourceRepo", "branch", "contextPath", "dockerfilePath", "composePath", "composeContent", "domain", "helmChart", "helmVersion", "helmRepository", "helmValues", "helmNamespace", "helmRelease"} {
			props[key] = text()
		}
		props["buildType"] = map[string]any{"type": "string", "enum": []string{"dockerfile", "compose", "helm"}}
		props["containerPort"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 65535}
		props["template"] = map[string]any{"type": "boolean"}
		props["helmValueOverrides"] = map[string]any{"type": "object"}
		props["healthPolicy"] = health()
		return object(props, []string{"projectId", "serverId", "name"})
	case "ServiceBindings":
		mapping := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
		helm := object(map[string]any{"keys": mapping, "secretNameValues": map[string]any{"type": "array", "items": text()}, "keyValues": mapping}, []string{"keys", "secretNameValues"})
		binding := object(map[string]any{"alias": text(), "serviceRef": text(), "environment": mapping, "compose": map[string]any{"type": "object", "additionalProperties": mapping}, "helm": helm}, []string{"alias", "serviceRef"})
		return map[string]any{"type": "array", "items": binding}
	case "HelmValuesInput":
		return object(map[string]any{"overrides": map[string]any{"type": "object"}}, []string{"overrides"})
	case "HealthPolicy":
		return health()
	case "DeploymentRollbackInput":
		props := map[string]any{"confirmDeploymentId": text(), "expectedCurrentDeploymentId": text(), "expectedReviewDigest": text(), "confirmDatabaseNotReverted": map[string]any{"type": "boolean", "const": true}}
		return object(props, []string{"confirmDeploymentId", "expectedCurrentDeploymentId", "expectedReviewDigest", "confirmDatabaseNotReverted"})
	case "ServerAdoptionInput":
		return object(map[string]any{"resourceId": text(), "revision": map[string]any{"type": "integer", "minimum": 1}, "confirmName": text()}, []string{"resourceId", "revision", "confirmName"})
	}
	return nil
}
