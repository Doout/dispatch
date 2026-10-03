package deploy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
	"github.com/oklog/ulid/v2"
)

// This uses a real local proxy and containers. Public DNS and ACME issuance
// remain separate operator-domain checks; this fixture makes no cloud calls.
func TestManagedDockerTraefikPromotionAndRestartIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_ROUTING_DOCKER_INTEGRATION") != "1" {
		t.Skip("set DISPATCH_ROUTING_DOCKER_INTEGRATION=1 for disposable local containers")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture docker command failed: %s: %s", args[0], out)
		}
		return strings.TrimSpace(string(out))
	}
	endpoint := os.Getenv("DOCKER_HOST")
	if endpoint == "" || os.Getenv("DOCKER_CONTEXT") != "" {
		endpoint = docker("context", "inspect", docker("context", "show"), "--format", "{{.Endpoints.docker.Host}}")
	}
	if !strings.HasPrefix(endpoint, "unix://") {
		t.Fatal("routing fixture requires a local Docker Unix socket")
	}
	owner := "routing-test-" + strings.ToLower(ulid.Make().String())
	app := core.App{ID: owner, ProjectID: owner, ServerID: owner, Name: owner, BuildType: core.BuildTypeDockerfile, ContainerPort: 8080,
		HealthPolicy: core.HealthPolicy{TimeoutSeconds: 10, IntervalSeconds: 1, FailureThreshold: 2, Checks: []core.HealthCheck{{ID: "ready", Kind: "http", Scope: "workload", Path: "/ready"}}}}
	server := core.Server{ID: owner, Name: "Fixture target", Runtime: core.ServerRuntimeDocker, Address: "local", Routing: &core.RoutingConfig{BaseDomain: "apps.example.test", EntryPoint: "fixture"}}
	directory := t.TempDir()
	publisher := &routing.FilePublisher{Directory: directory}
	executor := DockerExecutor{Routes: publisher}
	proxy := owner + "-proxy"
	images := []string{}
	proxyCreated := false
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		if err := executor.Cleanup(cleanup, app, server, func(core.DeploymentState, string) error { return nil }); err != nil {
			t.Error("owned candidate cleanup:", err)
		}
		if proxyCreated {
			out, err := exec.CommandContext(cleanup, "docker", "inspect", "--format", `{{index .Config.Labels "dispatch.test.owner"}}`, proxy).Output()
			if err != nil || strings.TrimSpace(string(out)) != owner {
				t.Error("fixture proxy ownership changed; container retained")
			} else if out, err = exec.CommandContext(cleanup, "docker", "rm", "-f", proxy).CombinedOutput(); err != nil {
				t.Errorf("fixture proxy cleanup: %s %v", out, err)
			}
		}
		for _, image := range images {
			out, err := exec.CommandContext(cleanup, "docker", "image", "inspect", "--format", `{{index .Config.Labels "dispatch.test.owner"}}`, image).Output()
			if err != nil || strings.TrimSpace(string(out)) != owner {
				t.Error("fixture image ownership changed; image retained")
				continue
			}
			if out, err := exec.CommandContext(cleanup, "docker", "image", "rm", image).CombinedOutput(); err != nil {
				t.Errorf("fixture image cleanup: %s %v", out, err)
			}
		}
	})
	build := func(revision string, ready bool) string {
		t.Helper()
		repo := t.TempDir()
		files := map[string]string{"Dockerfile": "FROM busybox:1.37\nCOPY www /www\nCMD [\"httpd\",\"-f\",\"-p\",\"8080\",\"-h\",\"/www\"]\n", "www/index.html": owner + ":" + revision}
		if ready {
			files["www/ready"] = "ready"
		}
		for path, contents := range files {
			path = filepath.Join(repo, path)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
		}
		tag := owner + ":" + revision
		docker("build", "--label", "dispatch.test.owner="+owner, "-t", tag, repo)
		image := docker("image", "inspect", "--format", "{{.Id}}", tag)
		images = append(images, image)
		return image
	}
	first, bad, second := build("first", true), build("bad", false), build("second", true)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	proxyImage := os.Getenv("DISPATCH_TEST_TRAEFIK_IMAGE")
	if proxyImage == "" {
		proxyImage = "traefik:v3.6"
	}
	docker("run", "-d", "--name", proxy, "--label", "dispatch.test.owner="+owner, "--network", "host", "--volume", directory+":/etc/traefik/dispatch-routes:ro", proxyImage,
		fmt.Sprintf("--entrypoints.fixture.address=127.0.0.1:%d", port), "--providers.file.directory=/etc/traefik/dispatch-routes", "--providers.file.watch=true", "--log.level=ERROR")
	proxyCreated = true
	var hostname string
	deploy := func(id, image string, succeeds bool) {
		t.Helper()
		d := core.Deployment{ID: id, AppID: app.ID}
		plan, err := routing.Plan(d, app, server)
		if err != nil || plan == nil {
			t.Fatal("route plan", err)
		}
		hostname = plan.Hostname
		if _, err = publisher.Prepare(ctx, *plan); err != nil {
			t.Fatal(err)
		}
		err = executor.startRoutedDocker(ctx, d, app, server, *plan, image, "", func(core.DeploymentState, string) error { return nil })
		if succeeds && err != nil || !succeeds && err == nil {
			t.Fatalf("candidate %s result: %v", id, err)
		}
	}
	client := &http.Client{Timeout: 2 * time.Second}
	assertServes := func(expected string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
			request.Host = hostname
			response, err := client.Do(request)
			if err == nil {
				body, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
				response.Body.Close()
				if readErr == nil && response.StatusCode == 200 && string(body) == owner+":"+expected {
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("proxy did not serve the accepted revision:", expected)
	}
	deploy("first", first, true)
	assertServes("first")
	deploy("bad", bad, false)
	assertServes("first")
	deploy("second", second, true)
	assertServes("second")
	current, err := publisher.Read(ctx, app.ID)
	if err != nil || current.DeploymentID != "second" || current.PreviousDeploymentID != "first" {
		t.Fatal("route history did not retain the serving predecessor", err)
	}
	// Reload the persistent route through a new publisher and a restarted proxy.
	reopened := &routing.FilePublisher{Directory: directory}
	saved, err := reopened.Read(ctx, app.ID)
	if err != nil || saved.DeploymentID != current.DeploymentID || saved.Destination != current.Destination {
		t.Fatal("route identity changed after reopening", err)
	}
	docker("restart", proxy)
	assertServes("second")
	t.Log("real Docker candidates and Traefik preserved the healthy release through rejection, promotion and proxy restart")
}
