package deploy

import "errors"

// Cluster resources and custom resources require a separate administrator path.
// The registered target contract covers these built-in namespaced workloads.
func enforceHelmNamespace(document, metadata map[string]any, namespace string) error {
	api, _ := document["apiVersion"].(string)
	kind, _ := document["kind"].(string)
	allowed := map[string]map[string]bool{
		"v1":                           {"ConfigMap": true, "Secret": true, "Service": true, "Pod": true, "PersistentVolumeClaim": true, "ServiceAccount": true},
		"apps/v1":                      {"Deployment": true, "StatefulSet": true, "DaemonSet": true, "ReplicaSet": true},
		"batch/v1":                     {"Job": true, "CronJob": true},
		"networking.k8s.io/v1":         {"Ingress": true, "NetworkPolicy": true},
		"policy/v1":                    {"PodDisruptionBudget": true},
		"autoscaling/v2":               {"HorizontalPodAutoscaler": true},
		"rbac.authorization.k8s.io/v1": {"Role": true, "RoleBinding": true},
	}
	if !allowed[api][kind] {
		return errors.New("The chart contains a cluster resource, custom resource or unsupported API. Registered namespace targets accept supported built-in namespaced resources only.")
	}
	if selected, exists := metadata["namespace"]; exists && selected != "" && selected != namespace {
		return errors.New("The chart contains a resource outside the target's registered namespace.")
	}
	metadata["namespace"] = namespace
	return nil
}
