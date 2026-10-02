package deploy

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

const DefaultServiceImage = "postgres:17-bookworm"
const DefaultServiceNetwork = "dispatch-services"

var provisionReference = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

func ServiceResourceName(runID string) string { return "dispatch-svc-" + strings.ToLower(runID) }

func ValidateServiceTarget(server core.Server, provider string) error {
	switch provider {
	case "docker":

		if server.Runtime != core.ServerRuntimeDocker {
			return errors.New("Docker provisioning requires a Docker deployment server")
		}
		if server.AgentNodeID == "" && server.Address != "local" && server.Address != "localhost" && server.Address != "127.0.0.1" {
			return errors.New("Docker provisioning currently requires a local enrolled server")
		}
	case "helm":
		if !core.IsKubernetesRuntime(server.Runtime) || server.Kubernetes == nil || server.Kubernetes.KubeconfigPath == "" && server.Kubernetes.KubeconfigData == "" {
			return errors.New("Helm provisioning requires a Kubernetes or OpenShift server")
		}
	default:
		return errors.New("unknown service provisioner")
	}
	return nil
}

// ProvisionReferences validates declarative references before any resources are created.
func ProvisionReferences(value string, inputs map[string]bool) error {
	for _, match := range provisionReference.FindAllStringSubmatch(value, -1) {
		ref := strings.TrimSpace(match[1])
		if strings.HasPrefix(ref, "inputs.") && inputs[strings.TrimPrefix(ref, "inputs.")] {
			continue
		}
		switch ref {
		case "service.name", "service.resource", "service.namespace", "service.database", "service.username", "service.password":
		default:
			return fmt.Errorf("unknown provisioner reference %s", ref)
		}
	}
	if strings.Contains(provisionReference.ReplaceAllString(value, ""), "{{") || strings.Contains(provisionReference.ReplaceAllString(value, ""), "}}") {
		return errors.New("invalid provisioner reference")
	}
	return nil
}

func provisionVariables(req core.ServiceProvisionRequest, namespace string) (map[string]string, error) {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return nil, errors.New("cannot generate service credentials")
	}
	database, username := req.Inputs["database"], req.Inputs["username"]
	if database == "" {
		database = req.Run.ServiceName
	}
	if username == "" {
		username = "dispatch"
	}
	if req.ServiceType == "postgresql" && (len(database) > 63 || len(username) > 63 || strings.ContainsRune(database, 0) || strings.ContainsRune(username, 0)) {
		return nil, errors.New("PostgreSQL database and username inputs must be at most 63 bytes and contain no NUL")
	}
	values := map[string]string{"service.name": req.Run.ServiceName, "service.resource": ServiceResourceName(req.Run.ID), "service.namespace": namespace, "service.database": database, "service.username": username, "service.password": hex.EncodeToString(bytes)}
	for key, value := range req.Inputs {
		values["inputs."+key] = value
	}
	return values, nil
}

func provisionString(value string, variables map[string]string) (string, error) {
	var unknown bool
	result := provisionReference.ReplaceAllStringFunc(value, func(ref string) string {
		key := strings.TrimSpace(provisionReference.FindStringSubmatch(ref)[1])
		value, ok := variables[key]
		if !ok {
			unknown = true
		}
		return value
	})
	if unknown {
		return "", errors.New("a provisioner reference is unavailable")
	}
	return result, nil
}

func provisionValue(value any, variables map[string]string) (any, error) {
	switch value := value.(type) {
	case string:
		return provisionString(value, variables)
	case map[string]any:
		result := map[string]any{}
		for key, field := range value {
			resolved, err := provisionValue(field, variables)
			if err != nil {
				return nil, err
			}
			result[key] = resolved
		}
		return result, nil
	case []any:
		result := make([]any, len(value))
		for i, field := range value {
			resolved, err := provisionValue(field, variables)
			if err != nil {
				return nil, err
			}
			result[i] = resolved
		}
		return result, nil
	default:
		return value, nil
	}
}

func provisionOutputs(req core.ServiceProvisionRequest, configured map[string]string, variables map[string]string, host string) (map[string]string, error) {
	fields := map[string]string{}
	if req.ServiceType == "postgresql" && len(configured) == 0 {
		fields = map[string]string{"host": host, "port": "5432", "database": variables["service.database"], "username": variables["service.username"], "password": variables["service.password"], "sslmode": "disable"}
		fields["connectionUrl"] = postgresProvisionURL(fields)
	} else {
		for key, value := range configured {
			resolved, err := provisionString(value, variables)
			if err != nil {
				return nil, err
			}
			fields[key] = resolved
		}
	}
	result := map[string]string{}
	for _, name := range req.Outputs {
		value, ok := fields[name]
		if !ok {
			return nil, fmt.Errorf("provisioner connection is missing %s", name)
		}
		result[name] = value
	}
	return result, nil
}

func provisionLabels(req core.ServiceProvisionRequest) map[string]string {
	return map[string]string{"dispatch.managed-by": "dispatch", "dispatch.project": req.Run.ProjectID, "dispatch.service-template": req.Run.TemplateID, "dispatch.service-provision": req.Run.ID}
}
