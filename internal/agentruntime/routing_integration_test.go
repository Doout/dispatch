package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/oklog/ulid/v2"
)

func TestRemoteManagedRouteCandidateIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set DISPATCH_RUNTIME_INTEGRATION=1 for disposable Docker candidates")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	appID := "route-test-" + strings.ToLower(ulid.Make().String())
	app := core.App{ID: appID, ProjectID: "project", ServerID: "server", Name: "Route integration", BuildType: core.BuildTypeCompose, ContainerPort: 8080, HealthPolicy: core.HealthPolicy{TimeoutSeconds: 15, CheckTimeoutSeconds: 2, IntervalSeconds: 1, FailureThreshold: 3, Checks: []core.HealthCheck{{ID: "http", Kind: "http", Path: "/", Port: 8080}}}}
	server := core.Server{ID: "server", AgentNodeID: "node", Runtime: core.ServerRuntimeDocker, Address: "agent:node", Routing: &core.RoutingConfig{BaseDomain: "apps.example.com"}}
	directory := t.TempDir()
	publisher := &routing.FilePublisher{Directory: filepath.Join(directory, "routes")}
	if err := os.MkdirAll(publisher.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyPort := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	proxyName := appID + "-proxy"
	proxyCommand := exec.CommandContext(ctx, "docker", "run", "-d", "--name", proxyName, "--network", "host", "--mount", "type=bind,src="+publisher.Directory+",dst=/routes,readonly", "traefik:v3.6", "--entrypoints.web.address=127.0.0.1:"+fmt.Sprint(proxyPort), "--providers.file.directory=/routes", "--providers.file.watch=true", "--log.level=ERROR")
	if out, err := proxyCommand.CombinedOutput(); err != nil {
		t.Fatalf("start route proxy: %s %v", out, err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", proxyName).Run() })
	publicClient := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(proxyPort)))
	}}}
	defer publicClient.CloseIdleConnections()
	assertPublic := func(route core.ApplicationRoute, want string) {
		t.Helper()
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			checked := (routing.Probe{Client: publicClient, LookupHost: func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }}).Check(ctx, route)
			if checked.State == "active" {
				response, err := publicClient.Get("http://" + route.Hostname)
				if err == nil {
					body, _ := io.ReadAll(io.LimitReader(response.Body, 100))
					response.Body.Close()
					if strings.TrimSpace(string(body)) == want {
						return
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("public proxy never served %s for %s", want, route.DeploymentID)
	}
	worker, err := Open(filepath.Join(directory, "state"), "node", Options{RoutingDirectory: publisher.Directory})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	t.Cleanup(func() {
		out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=dispatch.app="+appID).Output()
		for _, id := range strings.Fields(string(out)) {
			_ = exec.Command("docker", "rm", "-f", id).Run()
		}
		out, _ = exec.Command("docker", "network", "ls", "-q", "--filter", "label=dispatch.app="+appID).Output()
		for _, id := range strings.Fields(string(out)) {
			_ = exec.Command("docker", "network", "rm", id).Run()
		}
	})
	deployCandidate := func(version string, healthy bool) (remoteruntime.Result, remoteruntime.LeasedJob) {
		t.Helper()
		command := "mkdir -p /www; echo " + version + " > /www/index.html; httpd -f -p 8080 -h /www"
		if !healthy {
			command = "sleep 300"
		}
		app.ComposeContent = "services:\n  web:\n    image: busybox:1.37\n    command: ['sh', '-c', '" + command + "']\n"
		d := core.Deployment{ID: ulid.Make().String(), AppID: app.ID, SpecDigest: app.SpecDigest(), Health: core.DeploymentHealth{Policy: app.HealthPolicy}}
		request := remoteruntime.NewRequest(runtimecontract.Deploy, d, app, server)
		raw, _ := json.Marshal(request)
		digest := sha256.Sum256(raw)
		job := remoteruntime.LeasedJob{ID: ulid.Make().String(), Attempt: 1, Digest: hex.EncodeToString(digest[:]), ExpiresAt: time.Now().Add(time.Minute), Request: request}
		return worker.Run(ctx, job, nil), job
	}
	readVersion := func(destination, want string) {
		t.Helper()
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Get(destination)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, 100))
		if strings.TrimSpace(string(body)) != want {
			t.Fatalf("candidate body %q want %q", body, want)
		}
	}
	first, job := deployCandidate("first", true)
	if first.State != "succeeded" || first.Route == nil || first.Route.State != "published" {
		t.Fatalf("first route: %#v", first)
	}
	readVersion(first.Route.Destination, "first")
	assertPublic(*first.Route, "first")
	replay := worker.Run(ctx, job, nil)
	if replay.Route == nil || replay.Route.Destination != first.Route.Destination {
		t.Fatal("receipt replay changed candidate")
	}
	failed, _ := deployCandidate("broken", false)
	if failed.State != "failed" || failed.Route == nil || failed.Route.DeploymentID != first.Route.DeploymentID {
		t.Fatalf("failed candidate route evidence: %#v", failed)
	}
	current, err := publisher.Read(ctx, app.ID)
	if err != nil || current.DeploymentID != first.Route.DeploymentID {
		t.Fatal("failed candidate replaced live route", current, err)
	}
	readVersion(current.Destination, "first")
	assertPublic(current, "first")
	second, _ := deployCandidate("second", true)
	if second.State != "succeeded" || second.Route == nil || second.Route.PreviousDeploymentID != first.Route.DeploymentID || second.Route.Destination == first.Route.Destination {
		t.Fatalf("candidate switch: %#v", second)
	}
	readVersion(second.Route.Destination, "second")
	assertPublic(*second.Route, "second")
	readVersion(second.Route.PreviousDestination, "first")

	runRequest := func(request remoteruntime.Request) remoteruntime.Result {
		t.Helper()
		raw, _ := json.Marshal(request)
		digest := sha256.Sum256(raw)
		result := worker.Run(ctx, remoteruntime.LeasedJob{ID: ulid.Make().String(), Attempt: 1, Digest: hex.EncodeToString(digest[:]), ExpiresAt: time.Now().Add(time.Minute), Request: request}, nil)
		if result.State != "succeeded" {
			t.Fatalf("%s failed: %#v", request.Operation, result)
		}
		return result
	}
	reviewRequest := remoteruntime.NewRequest(runtimecontract.Inspect, core.Deployment{}, app, server)
	reviewRequest.SourceDeploymentID = first.Route.DeploymentID
	reviewed := runRequest(reviewRequest)
	if reviewed.Rollback == nil || !reviewed.Rollback.Available || reviewed.RuntimeDigest == "" {
		t.Fatal("missing managed rollback evidence", reviewed)
	}
	rollbackDeployment := core.Deployment{ID: ulid.Make().String(), AppID: app.ID, SpecDigest: app.SpecDigest(), Health: core.DeploymentHealth{Policy: app.HealthPolicy}}
	rollbackRequest := remoteruntime.NewRequest(runtimecontract.Rollback, rollbackDeployment, app, server)
	rollbackRequest.SourceDeploymentID = first.Route.DeploymentID
	rollbackRequest.ExpectedRuntime = reviewed.RuntimeDigest
	restored := runRequest(rollbackRequest)
	if restored.Route == nil || restored.Route.PreviousDeploymentID != second.Route.DeploymentID {
		t.Fatal("rollback lost route history", restored)
	}
	assertPublic(*restored.Route, "first")
	if len(restored.Resources) != 2 {
		t.Fatal("older managed candidates were not pruned", restored.Resources)
	}
}
