package deploy

import (
	"errors"
	"fmt"

	"github.com/doout/dispatch/internal/core"
)

// Kubernetes readiness removes failed candidates from Service endpoints while
// Helm's atomic wait retains/recovers the previously successful release. The
// chart remains responsible for its rollout strategy and ingress ownership.
func configureHelmHealth(object map[string]any, policy core.HealthPolicy, port int, domain string) (map[string]bool, error) {
	matched := map[string]bool{}
	if object["kind"] == "List" {
		items, _ := object["items"].([]any)
		for _, item := range items {
			child, _ := item.(map[string]any)
			found, err := configureHelmHealth(child, policy, port, domain)
			if err != nil {
				return nil, err
			}
			for key := range found {
				matched[key] = true
			}
		}
		return matched, nil
	}
	spec, _ := object["spec"].(map[string]any)
	if spec == nil {
		return matched, nil
	}
	switch object["kind"] {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
		template, _ := spec["template"].(map[string]any)
		spec, _ = template["spec"].(map[string]any)
	case "Pod":
	default:
		return matched, nil
	}
	containers, _ := spec["containers"].([]any)
	for _, item := range containers {
		container, _ := item.(map[string]any)
		name, _ := container["name"].(string)
		var selected *core.HealthCheck
		for _, check := range policy.Checks {
			if check.Kind == "container" || check.Kind == "tls" || check.Service != "" && check.Service != name {
				continue
			}
			if selected != nil {
				return nil, errors.New("Helm supports one HTTP or TCP policy probe per selected container")
			}
			copy := check
			selected = &copy
		}
		if selected == nil {
			continue
		}
		check := *selected
		checkPort := check.Port
		if checkPort == 0 {
			checkPort = port
		}
		if checkPort < 1 {
			return nil, errors.New("Helm health checks require an explicit container port")
		}
		probe := map[string]any{"periodSeconds": policy.IntervalSeconds, "timeoutSeconds": policy.CheckTimeoutSeconds, "failureThreshold": policy.FailureThreshold}
		switch check.Kind {
		case "http":
			request := map[string]any{"path": check.Path, "port": checkPort, "scheme": "HTTP"}
			if check.Scope == "route" {
				host, ok := healthDomain(domain)
				if !ok {
					return nil, errors.New("route health requires a valid application domain")
				}
				request["httpHeaders"] = []any{map[string]any{"name": "Host", "value": host}}
			}
			probe["httpGet"] = request
		case "tcp":
			probe["tcpSocket"] = map[string]any{"port": checkPort}
		default:
			return nil, fmt.Errorf("health check %s is unavailable for Helm", check.ID)
		}
		container["readinessProbe"] = probe
		matched[check.ID] = true
	}
	return matched, nil
}
