package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
)

func TestManagedDockerCandidateFailurePreservesServingRoute(t *testing.T) {
	for _, healthy := range []bool{false, true} {
		t.Run(map[bool]string{false: "unhealthy", true: "healthy"}[healthy], func(t *testing.T) {
			ctx := context.Background()
			app := core.App{ID: "app", ProjectID: "project", ServerID: "target", BuildType: core.BuildTypeDockerfile, ContainerPort: 8080, HealthPolicy: core.HealthPolicy{FailureThreshold: 1}}
			server := core.Server{ID: "target", Name: "Target", Runtime: "docker", Address: "local", Routing: &core.RoutingConfig{BaseDomain: "apps.example.com"}}
			publisher := &routing.FilePublisher{Directory: t.TempDir()}
			old, _ := routing.Plan(core.Deployment{ID: "old"}, app, server)
			if _, err := publisher.Prepare(ctx, *old); err != nil {
				t.Fatal(err)
			}
			if _, err := publisher.Promote(ctx, *old, routing.LoopbackDestination(32000)); err != nil {
				t.Fatal(err)
			}
			d := core.Deployment{ID: "new", AppID: app.ID}
			plan, _ := routing.Plan(d, app, server)
			if _, err := publisher.Prepare(ctx, *plan); err != nil {
				t.Fatal(err)
			}
			commands := []string{}
			executor := DockerExecutor{Routes: publisher, run: func(_ context.Context, _ io.Reader, output io.Writer, name string, args ...string) error {
				command := strings.Join(args, " ")
				commands = append(commands, command)
				if name != "docker" {
					return errors.New("unexpected command")
				}
				if len(args) > 0 && args[0] == "port" {
					_, _ = io.WriteString(output, "127.0.0.1:32444\n")
				}
				if len(args) > 0 && args[0] == "inspect" {
					if strings.Contains(command, "running") {
						raw, _ := json.Marshal(map[string]any{"running": healthy, "health": "none"})
						_, _ = output.Write(raw)
					} else {
						_, _ = io.WriteString(output, app.ID)
					}
				}
				return nil
			}}
			err := executor.startRoutedDocker(ctx, d, app, server, *plan, "sha256:image", "", func(core.DeploymentState, string) error { return nil })
			current, readErr := publisher.Read(ctx, app.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if healthy {
				if err != nil || current.DeploymentID != "new" || current.PreviousDeploymentID != "old" {
					t.Fatal("ready candidate not promoted", current, err)
				}
			} else if err == nil || current.DeploymentID != "old" || current.Destination != routing.LoopbackDestination(32000) {
				t.Fatal("unhealthy candidate replaced live route", current, err)
			}
			joined := strings.Join(commands, "\n")
			if !strings.Contains(joined, "-p 127.0.0.1::8080") || !strings.Contains(joined, "traefik.enable=false") || strings.Contains(joined, "rm -f dispatch-app") {
				t.Fatal("candidate exposed early or removed serving container", joined)
			}
		})
	}
}
func TestManagedComposeRejectsUnsafeStagingBeforeApply(t *testing.T) {
	app := core.App{ID: "app", ProjectID: "project", ContainerPort: 8080}
	server := core.Server{Routing: &core.RoutingConfig{BaseDomain: "apps.example.com"}}
	for _, field := range []string{"container_name", "network_mode", "writable_mount", "extra_port"} {
		t.Run(field, func(t *testing.T) {
			service := map[string]any{"image": "sha256:image"}
			config := map[string]any{"services": map[string]any{"web": service}}
			switch field {
			case "container_name":
				service[field] = "fixed"
			case "network_mode":
				service[field] = "host"
			case "writable_mount":
				service["volumes"] = []map[string]any{{"type": "volume", "source": "data", "target": "/data"}}
			case "extra_port":
				service["ports"] = []map[string]any{{"target": 9000}}
			}
			if _, err := routedComposeConfig(config, app, server, core.Deployment{ID: "new"}); err == nil {
				t.Fatal("unsafe Compose staging accepted")
			}
		})
	}
	config := map[string]any{"services": map[string]any{"web": map[string]any{"image": "sha256:image", "ports": []map[string]any{{"target": 8080, "published": "8080", "host_ip": "0.0.0.0"}}}}}
	candidate, err := routedComposeConfig(config, app, server, core.Deployment{ID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(candidate)
	if strings.Contains(string(raw), "0.0.0.0") || !strings.Contains(string(raw), "127.0.0.1") || !strings.Contains(string(raw), `"traefik.enable":"false"`) {
		t.Fatal("Compose candidate has public ingress", string(raw))
	}
	original, _ := json.Marshal(config)
	if !strings.Contains(string(original), "0.0.0.0") {
		t.Fatal("staging mutated retained artifact")
	}
}
