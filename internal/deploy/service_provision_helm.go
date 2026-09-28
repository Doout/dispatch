package deploy

import (
	"context"
	"embed"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
)

//go:embed service-chart/Chart.yaml service-chart/templates/*.yaml
var postgresServiceChart embed.FS

func writeServiceChart(root string) (string, error) {
	for _, file := range []string{"Chart.yaml", "templates/postgresql.yaml"} {
		data, err := postgresServiceChart.ReadFile("service-chart/" + file)
		if err != nil {
			return "", err
		}
		path := filepath.Join(root, file)
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", err
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			return "", err
		}
	}
	return root, nil
}

func ServiceProvisionNamespace(spec core.HelmServiceProvision, server core.Server) string {
	if spec.Namespace != "" {
		return spec.Namespace
	}
	if server.Kubernetes != nil && server.Kubernetes.Namespace != "" {
		return server.Kubernetes.Namespace
	}
	return "default"
}

// Provision installs a chart through the existing Helm SDK, including wait,
// atomic rollback and release provenance. PostgreSQL has a bundled default chart.
func (e HelmExecutor) Provision(ctx context.Context, req core.ServiceProvisionRequest, spec core.HelmServiceProvision, server core.Server) (map[string]string, error) {
	if err := ValidateServiceTarget(server, "helm"); err != nil {
		return nil, err
	}
	namespace := ServiceProvisionNamespace(spec, server)
	variables, err := provisionVariables(req, namespace)
	if err != nil {
		return nil, err
	}
	name := variables["service.resource"]
	outputs, err := provisionOutputs(req, spec.Connection, variables, name+"."+namespace+".svc")
	if err != nil {
		return nil, err
	}
	chart := spec.Chart
	values := map[string]any{}
	root, err := os.MkdirTemp("", "dispatch-service-chart-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	if chart == "" {
		chart, err = writeServiceChart(root)
		if err != nil {
			return nil, errors.New("cannot load the built-in PostgreSQL chart")
		}
		image, storage := spec.Image, spec.Storage
		if image == "" {
			image = DefaultServiceImage
		}
		if storage == "" {
			storage = "10Gi"
		}
		values = map[string]any{"image": image, "storage": storage, "storageClass": spec.StorageClass, "database": variables["service.database"], "username": variables["service.username"], "password": variables["service.password"], "openShift": strings.EqualFold(server.Runtime, core.ServerRuntimeOpenShift)}
	} else {
		resolved, err := provisionValue(spec.Values, variables)
		if err != nil {
			return nil, err
		}
		if resolved != nil {
			values = resolved.(map[string]any)
		}
	}
	raw, err := yaml.Marshal(values)
	if err != nil {
		return nil, errors.New("invalid Helm provisioner values")
	}
	app := core.App{ID: req.Run.ID, ProjectID: req.Run.ProjectID, Name: name, BuildType: core.BuildTypeHelm, HelmChart: chart, HelmRepository: spec.Repository, HelmVersion: spec.Version, HelmRelease: name, HelmNamespace: namespace, HelmValues: string(raw), HelmProvenance: core.HelmProvenance{WorkflowResourceID: req.Run.TemplateID}}
	deployment := core.Deployment{ID: req.Run.ID, CommitSHA: req.ConfigSHA, CreatedAt: time.Now().UTC()}
	prepared, cleanup, err := prepareKubernetesServer(server)
	if err != nil {
		return nil, errors.New("cannot load the service target kubeconfig")
	}
	defer cleanup()
	client, err := e.client(prepared, namespace, root)
	if err != nil {
		return nil, errors.New("cannot initialize Helm for the service target")
	}
	if err := client.UpgradeInstall(ctx, name, app, deployment, values); err != nil {
		// Helm errors can contain rendered values or credentials. Keep the API error
		// useful without persisting the provider's raw response.
		return nil, errors.New("Helm service installation failed; check cluster access, storage and release status on the target server")
	}
	if err := client.Status(ctx, name); err != nil {
		return nil, errors.New("cannot verify the installed Helm service release")
	}
	return outputs, nil
}
