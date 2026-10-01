package deploy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
)

func TestComposeServiceNetworksPreserveApplicationNetworks(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "compose.yaml")
	for _, existing := range []string{"", "    networks: [app]\n", "    networks:\n      app:\n        aliases: [existing-alias]\n"} {
		doc := "services:\n  api:\n    image: example/app\n" + existing + "  unrelated:\n    image: example/other\nnetworks:\n  app: {}\n"
		if err := os.WriteFile(original, []byte(doc), 0600); err != nil {
			t.Fatal(err)
		}
		bindings := []core.ServiceRuntimeBinding{{DockerNetwork: "private-db", Binding: core.ServiceBinding{Compose: map[string]map[string]string{"api": {"DATABASE_URL": "url"}}}, Values: map[string]string{"url": "postgresql://user:credential@database/db"}}}
		override, err := composeServiceOverride(root, original, bindings)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(override)
		var result struct {
			Services map[string]struct {
				Networks map[string]any `yaml:"networks"`
			} `yaml:"services"`
			Networks map[string]map[string]any `yaml:"networks"`
		}
		if err := yaml.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Services) != 1 || len(result.Services["api"].Networks) != 2 || len(result.Networks) != 1 {
			t.Fatal("network override changed unrelated services or omitted existing networks")
		}
		expected := "app"
		if existing == "" {
			expected = "default"
		}
		if _, ok := result.Services["api"].Networks[expected]; !ok {
			t.Fatal("original application network was lost")
		}
		if strings.Contains(existing, "aliases:") && !reflect.DeepEqual(result.Services["api"].Networks["app"], map[string]any{"aliases": []any{"existing-alias"}}) {
			t.Fatal("original network aliases were lost")
		}
		for _, network := range result.Networks {
			if network["external"] != true || network["name"] != "private-db" {
				t.Fatal("service network is not external")
			}
		}
	}
	if err := os.WriteFile(original, []byte("services:\n  api:\n    image: example/app\n    network_mode: host\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := composeServiceOverride(root, original, []core.ServiceRuntimeBinding{{DockerNetwork: "private", Binding: core.ServiceBinding{Compose: map[string]map[string]string{"api": {}}}}}); err == nil {
		t.Fatal("accepted incompatible host networking")
	}
}

func TestDockerfileServiceNetworksAttachedBeforeStarting(t *testing.T) {
	repo := t.TempDir()
	serviceFixtureRepo(t, repo, map[string]string{"Dockerfile": "FROM scratch\n"})
	for _, networks := range [][]string{{"database"}, {"database", "cache"}} {
		var operations []string
		executor := DockerExecutor{run: func(ctx context.Context, stdin io.Reader, output io.Writer, name string, args ...string) error {
			if name == "git" {
				return command(ctx, stdin, output, name, args...)
			}
			if name == "docker" && len(args) > 0 && args[0] == "inspect" {
				_, _ = io.WriteString(output, `{"running":true,"health":"none"}`)
			}
			if name == "docker" {
				operations = append(operations, strings.Join(args, " "))
			}
			return nil
		}}
		app := core.App{ID: "network-app", Name: "application", SourceRepo: "file://" + repo, Branch: "main", BuildType: core.BuildTypeDockerfile, ContextPath: ".", DockerfilePath: "Dockerfile"}
		for _, network := range networks {
			app.ServiceRuntime = append(app.ServiceRuntime, core.ServiceRuntimeBinding{DockerNetwork: network, Binding: core.ServiceBinding{Environment: map[string]string{"DB": "url"}}, Values: map[string]string{"url": "connection"}})
		}
		if err := executor.Deploy(context.Background(), core.Deployment{ID: "deployment"}, app, core.Server{Address: "local"}, func(core.DeploymentState, string) error { return nil }); err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(operations, "\n")
		if len(networks) == 1 && !strings.Contains(joined, "run -d --name dispatch-network-app") {
			t.Fatal("single network did not use ordinary Docker run")
		}
		if len(networks) > 1 {
			create, connect, start := -1, -1, -1
			for i, op := range operations {
				if strings.HasPrefix(op, "create ") {
					create = i
				}
				if strings.HasPrefix(op, "network connect ") {
					connect = i
				}
				if strings.HasPrefix(op, "start ") {
					start = i
				}
			}
			if create < 0 || connect <= create || start <= connect {
				t.Fatal("application started before all bound service networks were attached")
			}
		}
		if !strings.Contains(joined, "--network ") {
			t.Fatal("Docker app did not join its service network")
		}
	}
}
