package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/doout/dispatch/internal/deploy"
	"k8s.io/apimachinery/pkg/api/resource"
)

var serviceNamespace = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
var provisionNetwork = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
var provisionEnvironment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validateBuiltinProvision(spec ServiceTemplateSpec) error {
	p := spec.Provision
	if p.Docker != nil && p.Helm != nil || strings.TrimSpace(p.Run) != "" {
		return errors.New("spec.provision must choose exactly one of docker, helm, or run")
	}
	if p.RunFrom != "" || len(p.Sources) != 0 || len(p.SourcePaths) != 0 || p.Builder != "" || len(p.Secrets) != 0 || len(spec.Sources) != 0 {
		return errors.New("built-in provisioners do not use job sources, builders or secret bindings; use inputs for provider values")
	}
	inputs := map[string]bool{}
	for name := range spec.Inputs {
		inputs[name] = true
	}
	check := func(value string) error { return deploy.ProvisionReferences(value, inputs) }
	connection := map[string]string{}
	if d := p.Docker; d != nil {
		if strings.TrimSpace(d.ServerRef) == "" {
			return errors.New("spec.provision.docker.serverRef is required")
		}
		if d.Network != "" && !provisionNetwork.MatchString(d.Network) {
			return errors.New("spec.provision.docker.network is invalid")
		}
		if d.Image != "" && (strings.HasPrefix(d.Image, "-") || strings.ContainsAny(d.Image, " \t\r\n\x00{}")) {
			return errors.New("spec.provision.docker.image must be a literal image reference")
		}
		if d.StorageMountPath != "" && (!path.IsAbs(d.StorageMountPath) || path.Clean(d.StorageMountPath) != d.StorageMountPath || strings.ContainsAny(d.StorageMountPath, ",\r\n\x00")) {
			return errors.New("spec.provision.docker.storageMountPath must be an absolute container directory")
		}
		if spec.ServiceType == "generic" && (d.Image == "" || len(d.Healthcheck) == 0) {
			return errors.New("generic Docker services require an image and healthcheck")
		}
		for name, value := range d.Environment {
			if !provisionEnvironment.MatchString(name) {
				return fmt.Errorf("invalid Docker environment name %s", name)
			}
			if spec.ServiceType == "postgresql" && (strings.HasPrefix(name, "POSTGRES_") || name == "PGDATA") {
				return errors.New("PostgreSQL initialization variables are managed by Dispatch")
			}
			if err := check(value); err != nil {
				return err
			}
		}
		for _, value := range d.Healthcheck {
			if err := check(value); err != nil {
				return err
			}
		}
		connection = d.Connection
	}
	if h := p.Helm; h != nil {
		if strings.TrimSpace(h.ServerRef) == "" {
			return errors.New("spec.provision.helm.serverRef is required")
		}
		if h.Namespace != "" && !serviceNamespace.MatchString(h.Namespace) {
			return errors.New("spec.provision.helm.namespace is invalid")
		}
		if h.Chart == "" {
			if spec.ServiceType != "postgresql" {
				return errors.New("generic Helm services require a chart")
			}
			if h.Repository != "" || h.Version != "" || len(h.Values) != 0 || len(h.Connection) != 0 {
				return errors.New("custom chart values and connections require spec.provision.helm.chart")
			}
			if h.Storage != "" {
				amount, err := resource.ParseQuantity(h.Storage)
				if err != nil || amount.Sign() <= 0 {
					return errors.New("spec.provision.helm.storage must be a positive storage quantity")
				}
			}
		} else {
			if h.Image != "" || h.Storage != "" || h.StorageClass != "" {
				return errors.New("custom Helm charts configure image and storage through values")
			}
			if !strings.HasPrefix(h.Chart, "oci://") && !strings.HasPrefix(h.Chart, "https://") && h.Repository == "" {
				return errors.New("custom Helm charts require an OCI reference or repository URL")
			}
			if h.Repository != "" && (!strings.HasPrefix(h.Repository, "https://") || strings.Contains(h.Chart, "://")) {
				return errors.New("Helm repository charts require an HTTPS repository and a relative chart name")
			}
			raw, err := json.Marshal(h.Values)
			if err != nil {
				return err
			}
			var value any
			if err = json.Unmarshal(raw, &value); err != nil {
				return err
			}
			var walk func(any) error
			walk = func(v any) error {
				switch v := v.(type) {
				case string:
					return check(v)
				case []any:
					for _, item := range v {
						if err := walk(item); err != nil {
							return err
						}
					}
				case map[string]any:
					for _, item := range v {
						if err := walk(item); err != nil {
							return err
						}
					}
				}
				return nil
			}
			if err := walk(value); err != nil {
				return err
			}
		}
		connection = h.Connection
	}
	custom := spec.ServiceType == "generic" || p.Helm != nil && p.Helm.Chart != "" || len(connection) > 0
	if custom {
		if len(connection) != len(spec.Outputs) {
			return errors.New("provisioner connection must map every declared output")
		}
		for name := range spec.Outputs {
			value, ok := connection[name]
			if !ok {
				return fmt.Errorf("provisioner connection is missing %s", name)
			}
			if err := check(value); err != nil {
				return err
			}
			sensitive := false
			for _, match := range templatePattern.FindAllStringSubmatch(value, -1) {
				ref := strings.TrimSpace(match[1])
				if ref == "service.password" {
					sensitive = true
				}
				if strings.HasPrefix(ref, "inputs.") {
					field := spec.Inputs[strings.TrimPrefix(ref, "inputs.")]
					if field.Type == "secret" || field.Type == "service" {
						sensitive = true
					}
				}
			}
			if sensitive && !spec.Outputs[name].Sensitive && name != "password" && !(spec.ServiceType == "postgresql" && name == "connectionUrl") {
				return fmt.Errorf("spec.outputs.%s must be sensitive because its connection mapping uses credentials", name)
			}
		}
	} else {
		if _, ok := spec.Outputs["caCert"]; ok {
			return errors.New("the built-in PostgreSQL service does not configure TLS; use a custom chart or provider for TLS")
		}
	}
	return nil
}
