package deploy

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
)

func provisionRequest() core.ServiceProvisionRequest {
	return core.ServiceProvisionRequest{Run: core.ServiceProvisionRun{ID: "01M4EXAMPLE0000000000000000", TemplateID: "template", ProjectID: "project", ServiceName: "orders"}, ServiceType: "postgresql", Outputs: []string{"connectionUrl"}, Inputs: map[string]string{}, ConfigSHA: "sha"}
}

func TestDockerServiceProvisionGeneratedCredentialsAndPrivateStorage(t *testing.T) {
	var commands [][]string
	var environment, envFile string
	executor := DockerExecutor{run: func(ctx context.Context, _ io.Reader, output io.Writer, command string, args ...string) error {
		if command != "docker" {
			t.Fatal("provisioning invoked custom code")
		}
		commands = append(commands, args)
		if args[0] == "run" {
			for i, arg := range args {
				if arg == "--env-file" {
					envFile = args[i+1]
					data, err := os.ReadFile(envFile)
					if err != nil {
						t.Fatal(err)
					}
					environment = string(data)
					info, _ := os.Stat(envFile)
					if info.Mode().Perm() != 0600 {
						t.Fatal("credentials file is not private")
					}
				}
			}
		}
		if args[0] == "inspect" {
			_, _ = io.WriteString(output, `{"Status":"running","Health":{"Status":"healthy"}}`)
		}
		return nil
	}}
	req := provisionRequest()
	outputs, err := executor.Provision(context.Background(), req, core.DockerServiceProvision{}, core.Server{Runtime: "docker", Address: "local"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(outputs["connectionUrl"])
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if len(password) != 48 || !strings.Contains(environment, "POSTGRES_PASSWORD="+password) || u.Hostname() != ServiceResourceName(req.Run.ID) || u.Path != "/orders" {
		t.Fatal("incorrect generated connection")
	}
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Fatal("credential file was retained")
	}
	all := ""
	for _, args := range commands {
		all += strings.Join(args, " ") + "\n"
	}
	for _, part := range []string{"volume create", "dispatch.service-template=template", "dispatch.service-provision=" + req.Run.ID, "--network dispatch-services", "--memory 512m", "--cpus 0.5", "--mount type=volume"} {
		if !strings.Contains(all, part) {
			t.Errorf("missing %s", part)
		}
	}
	if strings.Contains(all, password) || strings.Contains(all, " -p ") || strings.Contains(all, "--publish") {
		t.Fatal("credentials or public ports in command arguments")
	}
}

func TestDockerServiceProvisionFailureCleansOnlyContainerAndRedactsError(t *testing.T) {
	removed, volumeRemoved := false, false
	executor := DockerExecutor{run: func(_ context.Context, _ io.Reader, output io.Writer, _ string, args ...string) error {
		if args[0] == "inspect" {
			_, _ = io.WriteString(output, `{"Status":"exited"}`)
		}
		if args[0] == "rm" {
			removed = true
		}
		if args[0] == "volume" && args[1] == "rm" {
			volumeRemoved = true
		}
		return nil
	}}
	if _, err := executor.Provision(context.Background(), provisionRequest(), core.DockerServiceProvision{}, core.Server{Runtime: "docker", Address: "local"}); err == nil || !strings.Contains(err.Error(), "readiness") {
		t.Fatal("missing failure")
	}
	if !removed || volumeRemoved {
		t.Fatal("unsafe failed provisioning cleanup")
	}
	req := provisionRequest()
	req.Inputs["database"] = "unsafe\nVALUE=1"
	invoked := false
	executor.run = func(context.Context, io.Reader, io.Writer, string, ...string) error {
		invoked = true
		return errors.New("secret response")
	}
	if _, err := executor.Provision(context.Background(), req, core.DockerServiceProvision{}, core.Server{Runtime: "docker", Address: "local"}); err == nil || invoked {
		t.Fatal("Docker invoked with newline-injected environment")
	}
}

func TestHelmServiceProvisionRendersBundledChartAndConnection(t *testing.T) {
	client := &recordingHelmClient{}
	executor := HelmExecutor{newClient: func(core.Server, string, string) (helmClient, error) { return client, nil }}
	req := provisionRequest()
	server := core.Server{Runtime: "kubernetes", Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/unused/config", Namespace: "databases"}}
	// Render while the temporary bundled chart still exists.
	executor.newClient = func(core.Server, string, string) (helmClient, error) {
		return &renderingProvisionHelmClient{recordingHelmClient: client, t: t}, nil
	}
	outputs, err := executor.Provision(context.Background(), req, core.HelmServiceProvision{}, server)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(outputs["connectionUrl"])
	password, _ := u.User.Password()
	if password != client.values["password"] || len(password) != 48 || u.Hostname() != ServiceResourceName(req.Run.ID)+".databases.svc" {
		t.Fatal("Helm outputs differ from the installed credentials")
	}
	if client.app.HelmProvenance.WorkflowResourceID != "template" || client.app.HelmNamespace != "databases" || client.operation != "upgrade-install,status" {
		t.Fatal("lost target or provenance")
	}
	if _, err := os.Stat(client.app.HelmChart); !os.IsNotExist(err) {
		t.Fatal("temporary chart retained")
	}
}

type renderingProvisionHelmClient struct {
	*recordingHelmClient
	t *testing.T
}

func (c *renderingProvisionHelmClient) UpgradeInstall(ctx context.Context, name string, app core.App, d core.Deployment, values map[string]any) error {
	chart, err := loader.Load(app.HelmChart)
	if err != nil {
		c.t.Fatal(err)
	}
	options, err := chartutil.ToRenderValues(chart, values, chartutil.ReleaseOptions{Name: name, Namespace: app.HelmNamespace, Revision: 1, IsInstall: true}, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	rendered, err := engine.Render(chart, options)
	if err != nil {
		c.t.Fatal(err)
	}
	manifest := rendered["dispatch-postgresql/templates/postgresql.yaml"]
	for _, part := range []string{"kind: PersistentVolumeClaim", "kind: Secret", "kind: StatefulSet", "type: ClusterIP", "readinessProbe:", "helm.sh/resource-policy: keep", "storage: \"10Gi\"", "runAsUser: 999"} {
		if !strings.Contains(manifest, part) {
			c.t.Errorf("missing %s", part)
		}
	}
	decoder := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var doc map[string]any
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			c.t.Fatal(err)
		}
	}
	return c.recordingHelmClient.UpgradeInstall(ctx, name, app, d, values)
}

func TestHelmServiceProvisionCustomChartResolvesInputsWithoutScripts(t *testing.T) {
	client := &recordingHelmClient{}
	executor := HelmExecutor{newClient: func(core.Server, string, string) (helmClient, error) { return client, nil }}
	req := provisionRequest()
	req.ServiceType = "generic"
	req.Outputs = []string{"endpoint", "token"}
	req.Inputs["region"] = "literal: 'quoted'\nnot: yaml"
	spec := core.HelmServiceProvision{Chart: "cache", Repository: "https://charts.example.test", Version: "1.2.3", Namespace: "db", Values: map[string]any{"nested": map[string]any{"region": "{{ inputs.region }}", "password": "{{ service.password }}"}}, Connection: map[string]string{"endpoint": "{{ service.resource }}.{{ service.namespace }}.svc", "token": "{{ service.password }}"}}
	result, err := executor.Provision(context.Background(), req, spec, core.Server{Runtime: "kubernetes", Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/unused/config"}})
	if err != nil {
		t.Fatal(err)
	}
	nested := client.values["nested"].(map[string]any)
	if nested["region"] != req.Inputs["region"] || nested["password"] != result["token"] || !reflect.DeepEqual(spec.Values["nested"], map[string]any{"region": "{{ inputs.region }}", "password": "{{ service.password }}"}) {
		t.Fatal("reference expansion mutated the template or interpreted input as YAML")
	}
	if client.app.HelmChart != "cache" || client.app.HelmVersion != "1.2.3" {
		t.Fatal("custom chart settings were lost")
	}
}

func TestBuiltinProvisionErrorsDoNotExposeProviderCredentials(t *testing.T) {
	docker := DockerExecutor{run: func(_ context.Context, _ io.Reader, _ io.Writer, _ string, args ...string) error {
		if args[0] == "run" {
			return errors.New("provider response includes secret-credential")
		}
		return nil
	}}
	_, err := docker.Provision(context.Background(), provisionRequest(), core.DockerServiceProvision{}, core.Server{Runtime: "docker", Address: "local"})
	if err == nil || strings.Contains(err.Error(), "secret-credential") {
		t.Fatal("Docker error was not redacted")
	}
	helm := HelmExecutor{newClient: func(core.Server, string, string) (helmClient, error) { return &failingProvisionHelmClient{}, nil }}
	_, err = helm.Provision(context.Background(), provisionRequest(), core.HelmServiceProvision{}, core.Server{Runtime: "kubernetes", Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/unused"}})
	if err == nil || strings.Contains(err.Error(), "secret-credential") {
		t.Fatal("Helm error was not redacted")
	}
}

type failingProvisionHelmClient struct{ recordingHelmClient }

func (failingProvisionHelmClient) UpgradeInstall(context.Context, string, core.App, core.Deployment, map[string]any) error {
	return errors.New("rendered Secret includes secret-credential")
}
