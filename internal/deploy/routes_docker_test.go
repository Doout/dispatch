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
					if strings.Contains(command, "{{.Image}}") {
						_, _ = io.WriteString(output, app.ID+"|"+app.ProjectID+"|"+d.ID+"|sha256:"+strings.Repeat("a", 64))
					} else if strings.Contains(command, "running") {
						raw, _ := json.Marshal(map[string]any{"running": healthy, "health": "none"})
						_, _ = output.Write(raw)
					} else {
						_, _ = io.WriteString(output, app.ID)
					}
				}
				return nil
			}}
			err := executor.startRoutedDocker(ctx, d, app, server, *plan, "sha256:"+strings.Repeat("a", 64), "", func(core.DeploymentState, string) error { return nil })
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

func TestManagedCandidateRejectsReplacedRevisionOrOwner(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	app := core.App{ID: "app", ProjectID: "project"}
	for _, metadata := range []string{"other|project|deployment|" + image, "app|other|deployment|" + image, "app|project|other|" + image, "app|project|deployment|sha256:" + strings.Repeat("b", 64)} {
		executor := DockerExecutor{run: func(_ context.Context, _ io.Reader, out io.Writer, _ string, _ ...string) error {
			_, err := io.WriteString(out, metadata)
			return err
		}}
		if err := executor.validateRouteCandidate(context.Background(), "candidate", app, "deployment", image); err == nil {
			t.Fatal("replaced candidate accepted", metadata)
		}
	}
}

func TestManagedCandidatePruningKeepsCurrentAndPrevious(t *testing.T) {
	removed := []string{}
	executor := DockerExecutor{run: func(_ context.Context, _ io.Reader, out io.Writer, _ string, args ...string) error {
		joined := strings.Join(args, " ")
		if args[0] == "ps" || strings.HasPrefix(joined, "network ls") {
			_, _ = io.WriteString(out, "current previous old")
			return nil
		}
		id := args[len(args)-1]
		if strings.Contains(joined, "inspect") {
			_, _ = io.WriteString(out, "app|project|"+id)
			return nil
		}
		if strings.Contains(joined, "rm") {
			removed = append(removed, joined)
		}
		return nil
	}}
	if err := executor.pruneRouteCandidates(context.Background(), core.App{ID: "app", ProjectID: "project"}, "current", "previous"); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || removed[0] != "rm -f old" || removed[1] != "network rm old" {
		t.Fatal("candidate retention changed serving resources", removed)
	}
}

func TestManagedCleanupRemovesCandidatesWithoutArtifactStore(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "foreign-project"}[foreign], func(t *testing.T) {
			app := core.App{ID: "app", ProjectID: "project", ServerID: "target", Name: "App", BuildType: core.BuildTypeDockerfile, ContainerPort: 8080}
			server := core.Server{ID: "target", Runtime: "docker", Address: "local", Routing: &core.RoutingConfig{BaseDomain: "apps.example.test"}}
			publisher := &routing.FilePublisher{Directory: t.TempDir()}
			plan, _ := routing.Plan(core.Deployment{ID: "current"}, app, server)
			if _, err := publisher.Prepare(context.Background(), *plan); err != nil {
				t.Fatal(err)
			}
			removed := []string{}
			executor := DockerExecutor{Routes: publisher, run: func(_ context.Context, _ io.Reader, out io.Writer, _ string, args ...string) error {
				if args[0] == "ps" {
					_, _ = io.WriteString(out, "candidate-current candidate-previous")
				} else if args[0] == "inspect" {
					project := app.ProjectID
					if foreign {
						project = "other-project"
					}
					_, _ = io.WriteString(out, app.ID+"|"+project+"|"+args[len(args)-1])
				} else if args[0] == "rm" {
					removed = append(removed, args[len(args)-1])
				}
				return nil
			}}
			err := executor.Cleanup(context.Background(), app, server, func(core.DeploymentState, string) error { return nil })
			if foreign {
				if err == nil || len(removed) != 0 {
					t.Fatal("foreign candidate cleanup was allowed", removed, err)
				}
			} else if err != nil || len(removed) != 3 || removed[0] != "candidate-current" || removed[1] != "candidate-previous" {
				t.Fatal("current or retained candidate leaked after cleanup", removed, err)
			}
		})
	}
}
