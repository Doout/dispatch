package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/serviceconn"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"time"
)

func (s *Service) ConfigureServices(vault *secretcrypto.Vault, secrets serviceconn.SecretResolver) {
	s.services = serviceconn.Resolver{Vault: vault, Secrets: secrets}
}
func (s *Service) resolveServices(ctx context.Context, d core.Deployment, app *core.App) ([]core.AppliedServiceBinding, error) {
	captured, err := s.store.GetDeploymentServiceBindings(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	resolved := map[string]map[string]string{}
	applied := []core.AppliedServiceBinding{}
	for _, c := range captured {
		if c.Service.ProjectID != app.ProjectID {
			return nil, errors.New("bound service is outside the application's project")
		}
		values, ok := resolved[c.Service.ID]
		if !ok {
			values, err = s.services.Resolve(ctx, c.Service)
			if err != nil {
				return nil, err
			}
			resolved[c.Service.ID] = values
		}
		sensitive := []string{}
		for key, field := range c.Service.Fields {
			if field.Sensitive || field.SecretRef != "" {
				sensitive = append(sensitive, values[key])
			}
		}
		if c.Service.Type == "postgresql" {
			sensitive = append(sensitive, values["connectionUrl"])
		}
		app.ServiceRuntime = append(app.ServiceRuntime, core.ServiceRuntimeBinding{Binding: c.Binding, Values: values, SensitiveValues: sensitive})
		applied = append(applied, core.AppliedServiceBinding{Alias: c.Binding.Alias, ServiceID: c.Service.ID, ServiceName: c.Service.Name, Revision: c.Service.Revision})
	}
	return applied, nil
}
func redactServiceMessage(message string, bindings []core.ServiceRuntimeBinding) string {
	values := []string{}
	for _, b := range bindings {
		for _, v := range b.SensitiveValues {
			if v != "" {
				values = append(values, v)
			}
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		message = strings.ReplaceAll(message, v, "[redacted]")
	}
	return message
}
func dockerServiceEnv(workspace string, bindings []core.ServiceRuntimeBinding) (string, error) {
	if len(bindings) == 0 {
		return "", nil
	}
	env := map[string]string{}
	for _, b := range bindings {
		for destination, field := range b.Binding.Environment {
			value, ok := b.Values[field]
			if !ok {
				return "", errors.New("service field is unavailable")
			}
			if strings.ContainsAny(value, "\r\n\x00") {
				return "", errors.New("Docker environment bindings cannot contain newlines or NUL; choose a single-line service field")
			}
			env[destination] = value
		}
	}
	names := []string{}
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	var content strings.Builder
	for _, name := range names {
		content.WriteString(name + "=" + env[name] + "\n")
	}
	path := filepath.Join(workspace, "dispatch-service.env")
	return path, os.WriteFile(path, []byte(content.String()), 0600)
}
func composeServiceOverride(workspace, composePath string, bindings []core.ServiceRuntimeBinding) (string, error) {
	if len(bindings) == 0 {
		return "", nil
	}
	original, err := os.ReadFile(composePath)
	if err != nil {
		return "", err
	}
	var doc struct {
		Services map[string]any `yaml:"services"`
	}
	if err = yaml.Unmarshal(original, &doc); err != nil {
		return "", errors.New("cannot parse Compose service names")
	}
	services := map[string]map[string]map[string]string{}
	for _, b := range bindings {
		for name, envs := range b.Binding.Compose {
			if _, ok := doc.Services[name]; !ok {
				return "", fmt.Errorf("Compose service %s does not exist", name)
			}
			if services[name] == nil {
				services[name] = map[string]map[string]string{"environment": {}}
			}
			for env, field := range envs {
				value, ok := b.Values[field]
				if !ok {
					return "", errors.New("service field is unavailable")
				}
				if strings.ContainsRune(value, 0) {
					return "", errors.New("environment values cannot contain NUL")
				}
				services[name]["environment"][env] = strings.ReplaceAll(value, "$", "$$")
			}
		}
	}
	payload, err := yaml.Marshal(map[string]any{"services": services})
	if err != nil {
		return "", err
	}
	path := filepath.Join(workspace, "dispatch-services.compose.yaml")
	return path, os.WriteFile(path, payload, 0600)
}
func serviceSecretName(deployment string, index int) string {
	return fmt.Sprintf("dispatch-svc-%s-%d", strings.ToLower(deployment), index)
}
func applyServiceValue(values map[string]interface{}, path, value string) error {
	parts := strings.Split(path, ".")
	current := values
	for _, key := range parts[:len(parts)-1] {
		if current[key] == nil {
			current[key] = map[string]interface{}{}
		}
		next, ok := current[key].(map[string]interface{})
		if !ok {
			return fmt.Errorf("service destination %s overlaps a scalar chart value", path)
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
	return nil
}
func helmServiceValues(d core.Deployment, app core.App, values map[string]interface{}) error {
	for i, b := range app.ServiceRuntime {
		if b.Binding.Helm == nil {
			return errors.New("invalid Helm service binding")
		}
		for _, path := range b.Binding.Helm.SecretNameValues {
			if err := applyServiceValue(values, path, serviceSecretName(d.ID, i)); err != nil {
				return err
			}
		}
		for path, key := range b.Binding.Helm.KeyValues {
			if err := applyServiceValue(values, path, key); err != nil {
				return err
			}
		}
	}
	return nil
}
func serviceKubeClient(server core.Server) (kubernetes.Interface, error) {
	path := server.Kubernetes.KubeconfigPath
	raw, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := clientcmd.NewNonInteractiveClientConfig(*raw, server.Kubernetes.Context, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, err
	}
	cfg.Timeout = 30 * time.Second
	return kubernetes.NewForConfig(cfg)
}
func createServiceSecrets(ctx context.Context, client kubernetes.Interface, d core.Deployment, app core.App, namespace, release string) error {
	if len(app.ServiceRuntime) == 0 {
		return nil
	}
	if _, err := client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		if _, err = client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	} else if err != nil {
		return err
	}
	for i, b := range app.ServiceRuntime {
		data := map[string][]byte{}
		for key, field := range b.Binding.Helm.Keys {
			value, ok := b.Values[field]
			if !ok {
				return errors.New("service field is unavailable")
			}
			data[key] = []byte(value)
		}
		immutable := true
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: serviceSecretName(d.ID, i), Namespace: namespace, Labels: map[string]string{"dispatch.app": app.ID, "dispatch.deployment": d.ID, "dispatch.service-binding": "true", "dispatch.release": release}}, Type: corev1.SecretTypeOpaque, Immutable: &immutable, Data: data}
		if _, err := client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return errors.New("cannot create application service Secret")
		}
	}
	return nil
}
func (e HelmExecutor) cleanupServiceSecrets(ctx context.Context, server core.Server, app core.App, namespace, release string) error {
	// Call only after Helm has successfully removed the release.
	if server.Kubernetes == nil {
		return nil
	}
	prepared, cleanup, err := prepareKubernetesServer(server)
	if err != nil {
		return err
	}
	defer cleanup()
	client, err := e.serviceClient(prepared)
	if err != nil {
		return err
	}
	return client.CoreV1().Secrets(namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: "dispatch.service-binding=true,dispatch.app=" + app.ID + ",dispatch.release=" + release})
}

func (e HelmExecutor) serviceClient(server core.Server) (kubernetes.Interface, error) {
	if e.newServiceClient != nil {
		return e.newServiceClient(server)
	}
	return serviceKubeClient(server)
}
