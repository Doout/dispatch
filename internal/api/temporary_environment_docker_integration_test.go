package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/agentruntime"
	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

// This fixture uses one disposable local Docker daemon. The HTTP route is real;
// its test resolver and dialer do not establish public DNS or ACME support.
func TestTemporaryEnvironmentDockerRestartExpiryIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set DISPATCH_RUNTIME_INTEGRATION=1 for disposable Docker environment expiry")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	prefix := "dispatch-environment-test-" + strings.ToLower(ulid.Make().String())
	directory := t.TempDir()
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	dockerHost := os.Getenv("DOCKER_HOST")
	if dockerHost == "" {
		dockerHost = docker("context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	}
	if !strings.HasPrefix(dockerHost, "unix://") {
		t.Fatal("the disposable environment fixture requires a local Docker Unix socket")
	}
	// Pull before accepting the finite lifetime; dependency downloads are setup.
	docker("pull", "busybox:1.37")
	docker("pull", "traefik:v3.6")
	sharedVolume, sharedContainer, ownedVolume := prefix+"-shared", prefix+"-service", prefix+"-retained"
	var ownedApp, ownedImage string
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		remove := func(args ...string) {
			if out, err := exec.CommandContext(cleanup, "docker", args...).CombinedOutput(); err != nil {
				t.Errorf("fixture cleanup %s: %v %s", args[0], err, out)
			}
		}
		if ownedApp != "" {
			out, err := exec.CommandContext(cleanup, "docker", "ps", "-aq", "--filter", "label=dispatch.app="+ownedApp).Output()
			if err != nil {
				t.Error("inspect fixture containers during cleanup", err)
			}
			for _, id := range strings.Fields(string(out)) {
				remove("rm", "-f", id)
			}
			out, err = exec.CommandContext(cleanup, "docker", "network", "ls", "-q", "--filter", "label=dispatch.app="+ownedApp).Output()
			if err != nil {
				t.Error("inspect fixture networks during cleanup", err)
			}
			for _, id := range strings.Fields(string(out)) {
				remove("network", "rm", id)
			}
		}
		for _, name := range []string{sharedContainer, prefix + "-proxy"} {
			if exec.CommandContext(cleanup, "docker", "container", "inspect", name).Run() == nil {
				remove("rm", "-f", name)
			}
		}
		for _, name := range []string{sharedVolume, ownedVolume} {
			if exec.CommandContext(cleanup, "docker", "volume", "inspect", name).Run() == nil {
				remove("volume", "rm", name)
			}
		}
		if ownedImage != "" {
			remove("image", "rm", "-f", ownedImage)
		}
	})
	docker("volume", "create", sharedVolume)
	docker("run", "-d", "--name", sharedContainer, "--mount", "type=volume,source="+sharedVolume+",target=/www", "busybox:1.37", "sh", "-c", "echo shared-data-survives > /www/index.html; exec httpd -f -p 8080 -h /www")
	sharedAddress := docker("inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", sharedContainer)
	routeDirectory := filepath.Join(directory, "routes")
	if err := os.MkdirAll(routeDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyAddress := listener.Addr().String()
	listener.Close()
	docker("run", "-d", "--name", prefix+"-proxy", "--network", "host", "--mount", "type=bind,src="+routeDirectory+",dst=/routes,readonly", "traefik:v3.6", "--entrypoints.web.address="+proxyAddress, "--providers.file.directory=/routes", "--providers.file.watch=true", "--log.level=ERROR")
	publicClient := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", proxyAddress)
	}}}
	defer publicClient.CloseIdleConnections()

	repo := filepath.Join(directory, "source")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"Dockerfile": "FROM busybox:1.37\nLABEL dispatch.test.owner=" + prefix + "\nCOPY serve.sh /serve.sh\nCMD [\"sh\",\"/serve.sh\"]\n",
		"serve.sh":   "set -eu\ntest \"$SHARED_TOKEN\" = captured-fixture-value\nmkdir -p /www\nwget -qO /www/index.html \"$SHARED_URL\"\nexec httpd -f -p 8080 -h /www\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Create isolated environment fixture")
	sourceSHA := git("rev-parse", "HEAD")
	keyPath := filepath.Join(directory, "controller.key")
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("!", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	var a *API
	var data *store.SQLStore
	openController := func() {
		t.Helper()
		var err error
		data, err = store.Open(ctx, filepath.Join(directory, "controller.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err = data.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		vault, err := secretcrypto.OpenFile(keyPath)
		if err != nil {
			t.Fatal(err)
		}
		broker := &remoteruntime.Broker{Store: data, Vault: vault}
		service := deploy.NewService(data, deploy.RemoteExecutor{Local: deploy.DockerExecutor{}, Broker: broker})
		service.Storage.Backend = deploy.RemoteStorageBackend{Broker: broker}
		a = New(data, service, false, AuthConfig{AdminToken: "secret"}, slog.New(slog.NewTextHandler(io.Discard, nil)), EventConfig{Vault: vault})
	}
	openController()
	defer func() { data.Close() }()
	project := core.Project{ID: prefix, Name: "Temporary acceptance", CreatedAt: time.Now().UTC()}
	if err = data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	var node core.PrivateNetwork
	if err = json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/private-networks", map[string]string{"name": prefix, "driver": "dispatch_agent"}, 201), &node); err != nil {
		t.Fatal(err)
	}
	session := enrollNodeTest(t, a, node)
	var target core.Server
	if err = json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/servers", map[string]string{"name": prefix, "projectId": project.ID, "runtime": "docker", "agentNodeId": node.ID}, 201), &target); err != nil {
		t.Fatal(err)
	}
	target.Routing = &core.RoutingConfig{BaseDomain: "environments.example.test", EntryPoint: "web"}
	if err = data.UpdateServer(ctx, target); err != nil {
		t.Fatal(err)
	}
	workerDirectory := filepath.Join(directory, "worker")
	worker, err := agentruntime.Open(workerDirectory, node.ID, agentruntime.Options{RoutingDirectory: routeDirectory})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { worker.Close() }()
	request := func(method, suffix string, input any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, "/api/v1/edge/nodes/"+node.ID+"/runtime/jobs/"+suffix, bytes.NewReader(raw)).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+session.Token)
		r.Header.Set("X-Dispatch-Runtime-Version", remoteruntime.APIVersion)
		r.Header.Set("X-Dispatch-Runtime-Capabilities", "deploy,inspect,logs,destroy,storage_inspect,storage_delete")
		out := httptest.NewRecorder()
		a.ServeHTTP(out, r)
		return out
	}
	if out := request("GET", "next", nil); out.Code != 204 {
		t.Fatal("runtime readiness", out.Code, out.Body.String())
	}
	template := core.App{ID: prefix + "-template", ProjectID: project.ID, ServerID: target.ID, Name: "Temporary template", BuildType: core.BuildTypeDockerfile, SourceRepo: "file://" + repo, Branch: "main", ContextPath: ".", DockerfilePath: "Dockerfile", ContainerPort: 8080, Template: true, State: "ready", CreatedAt: time.Now().UTC(), HealthPolicy: core.HealthPolicy{TimeoutSeconds: 20, CheckTimeoutSeconds: 2, IntervalSeconds: 1, FailureThreshold: 5, Checks: []core.HealthCheck{{ID: "http", Kind: "http", Path: "/", Port: 8080}}}}
	if err = data.CreateApp(ctx, template); err != nil {
		t.Fatal(err)
	}
	policy := core.InfrastructureQuotaPolicy{ProjectID: project.ID, Revision: 1, MaxTemporaryEnvironments: 1, MaxTemporaryLifetimeSeconds: 180, UpdatedAt: time.Now().UTC()}
	if err = data.SaveInfrastructureQuotaPolicy(ctx, policy, 0); err != nil {
		t.Fatal(err)
	}
	var shared core.Service
	sharedInput := map[string]any{"projectId": project.ID, "name": "shared-fixture", "type": "generic", "fields": map[string]any{"url": map[string]any{"value": "http://" + sharedAddress + ":8080"}, "token": map[string]any{"value": "captured-fixture-value", "sensitive": true}}}
	if err = json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/services", sharedInput, 201), &shared); err != nil {
		t.Fatal(err)
	}
	input := core.TemporaryEnvironmentInput{ProjectID: project.ID, TemplateID: template.ID, ServerID: target.ID, Name: "expiry-rehearsal", SourceSHA: sourceSHA, LifetimeSeconds: 60, ServiceBindings: []core.ServiceBinding{{Alias: "shared", ServiceRef: shared.ID, Environment: map[string]string{"SHARED_URL": "url", "SHARED_TOKEN": "token"}}}}
	var review core.TemporaryEnvironmentReview
	if err = json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/temporary-environments/review", input, 201), &review); err != nil {
		t.Fatal(err)
	}
	accept := temporaryAcceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: input.Name}
	out := temporaryKeyed(a, "/api/v1/temporary-environments", "actual-docker-environment", accept)
	if out.Code != 202 {
		t.Fatal(out.Code, out.Body.String())
	}
	receipt := decodeMutation(t, out)
	e, err := data.GetTemporaryEnvironment(ctx, review.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	ownedApp = e.AppID
	acceptedExpiry := e.ExpiresAt
	if e.SourceSHA != sourceSHA || e.DeploymentID != receipt.OperationID || e.Hostname == "" || e.AppID == template.ID {
		t.Fatal("accepted environment lost source or isolated identity", e)
	}
	docker("volume", "create", "--label", "dispatch.app="+e.AppID, "--label", "dispatch.project="+project.ID, ownedVolume)
	docker("run", "--rm", "--mount", "type=volume,source="+ownedVolume+",target=/data", "busybox:1.37", "sh", "-c", "echo retained-data-survives > /data/sentinel")
	// Move the branch and rotate the live registration after acceptance. The real
	// process must still use the captured commit and encrypted binding values.
	if err = os.WriteFile(filepath.Join(repo, "serve.sh"), []byte("exit 99\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-am", "Advance fixture branch")
	serviceRequestTest(t, a, "PUT", "/api/v1/services/"+shared.ID, map[string]any{"fields": map[string]any{"token": map[string]any{"value": "rotated-fixture-value"}}}, 200)
	var deployed remoteruntime.LeasedJob
	var executed remoteruntime.Result
	runNext := func() bool {
		t.Helper()
		out := request("GET", "next", nil)
		if out.Code == 204 {
			return false
		}
		if out.Code != 200 {
			t.Fatal("lease runtime work", out.Code, out.Body.String())
		}
		var leased remoteruntime.LeasedJob
		if err := json.Unmarshal(out.Body.Bytes(), &leased); err != nil {
			t.Fatal(err)
		}
		result := worker.Run(ctx, leased, nil)
		if result.State != "succeeded" {
			t.Fatalf("real worker %s failed: %s %s", leased.Request.Operation, result.State, result.Message)
		}
		if leased.Request.Operation == "deploy" {
			deployed, executed = leased, result
		}
		out = request("POST", leased.ID+"/complete", remoteruntime.Completion{LeaseToken: leased.LeaseToken, Result: result})
		if out.Code != 204 {
			t.Fatal("report real runtime result", out.Code, out.Body.String())
		}
		return true
	}
	pumpUntil := func(done func() bool) {
		t.Helper()
		for !done() {
			if err := ctx.Err(); err != nil {
				t.Fatal("runtime journey did not settle", err)
			}
			if !runNext() {
				time.Sleep(20 * time.Millisecond)
			}
		}
	}
	a.reconcileTemporaryEnvironments(ctx)
	pumpUntil(func() bool {
		d, err := data.GetDeployment(ctx, e.DeploymentID)
		if err != nil {
			t.Fatal(err)
		}
		if d.State.Terminal() && d.State != core.DeploymentSucceeded {
			t.Fatal("environment deployment failed", d.State, d.Message)
		}
		return d.State == core.DeploymentSucceeded
	})
	if executed.Health == nil || executed.Health.State != "passed" || executed.Health.Simulated || executed.Route == nil || deployed.Request.Deployment.CommitSHA != sourceSHA {
		t.Fatal("missing real source, health or route evidence")
	}
	containers := strings.Fields(docker("ps", "-aq", "--filter", "label=dispatch.app="+e.AppID))
	if len(containers) != 1 {
		t.Fatal("environment did not create exactly one workload", containers)
	}
	containerID := containers[0]
	ownedImage = docker("inspect", "--format", "{{.Image}}", containerID)
	if !strings.HasPrefix(ownedImage, "sha256:") || docker("exec", containerID, "cat", "/www/index.html") != "shared-data-survives" {
		t.Fatal("workload did not use the captured source and shared service")
	}
	route, err := data.GetApplicationRoute(ctx, e.AppID)
	if err != nil {
		t.Fatal(err)
	}
	probe := routing.Probe{Client: publicClient, LookupHost: func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }}
	deadline := time.Now().Add(10 * time.Second)
	for {
		checked := probe.Check(ctx, route)
		if checked.State == "active" {
			if err = data.SaveApplicationRouteObservation(ctx, route, checked); err != nil {
				t.Fatal(err)
			}
			response, err := publicClient.Get("http://" + route.Hostname)
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 128))
			response.Body.Close()
			if readErr != nil || response.StatusCode != 200 || strings.TrimSpace(string(body)) != "shared-data-survives" {
				t.Fatal("managed hostname did not serve the captured shared data", response.StatusCode, readErr)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("local Traefik route did not become active", checked.Message)
		}
		time.Sleep(50 * time.Millisecond)
	}
	a.reconcileTemporaryEnvironments(ctx)
	e, err = data.GetTemporaryEnvironment(ctx, e.ID)
	if err != nil || e.State != "ready" || !e.ExpiresAt.Equal(acceptedExpiry) {
		t.Fatal("environment did not become ready at its original deadline", e, err)
	}
	retainedID := deploy.StorageID(target.ID, "docker_volume", "", ownedVolume)
	volume, err := data.GetStorage(ctx, retainedID)
	if err != nil || volume.OwnerID != e.AppID || volume.Policy != "retain" || volume.State != "present" {
		t.Fatal("real retained volume was not inventoried", volume, err)
	}
	beforeIdentity := volume.Identity
	sharedRecorded, retainedRecorded := false, false
	for _, resource := range e.Resources {
		sharedRecorded = sharedRecorded || resource.Kind == "service" && resource.ID == shared.ID && resource.Ownership == "shared"
		retainedRecorded = retainedRecorded || resource.Kind == "docker_volume" && resource.ID == retainedID && resource.Ownership == "retained"
	}
	if !sharedRecorded || !retainedRecorded {
		t.Fatal("environment inventory omitted shared or retained ownership", e.Resources)
	}
	quota := serviceRequestTest(t, a, "GET", "/api/v1/projects/"+project.ID+"/infrastructure/quota", nil, 200)
	if !bytes.Contains(quota, []byte(`"temporaryEnvironments":1`)) {
		t.Fatal("ready environment did not retain its quota reservation", string(quota))
	}
	if err = a.deploy.CancelAndWaitOwnedApplication(ctx, e.AppID); err != nil {
		t.Fatal(err)
	}
	if err = worker.Close(); err != nil {
		t.Fatal(err)
	}
	if err = data.Close(); err != nil {
		t.Fatal(err)
	}
	openController()
	worker, err = agentruntime.Open(workerDirectory, node.ID, agentruntime.Options{RoutingDirectory: routeDirectory})
	if err != nil {
		t.Fatal(err)
	}
	replayed := worker.Run(ctx, deployed, nil)
	if replayed.State != "succeeded" || replayed.Route == nil || replayed.Route.Destination != executed.Route.Destination || docker("ps", "-aq", "--filter", "label=dispatch.app="+e.AppID) != containerID || docker("inspect", "--format", "{{.Image}}", containerID) != ownedImage {
		t.Fatal("worker restart duplicated or changed the accepted workload")
	}
	out = temporaryKeyed(a, "/api/v1/temporary-environments", "actual-docker-environment", accept)
	if out.Code != 202 || decodeMutation(t, out).OperationID != receipt.OperationID {
		t.Fatal("controller restart lost original creation receipt", out.Code, out.Body.String())
	}
	e, err = data.GetTemporaryEnvironment(ctx, e.ID)
	if err != nil || e.State != "ready" || !e.ExpiresAt.Equal(acceptedExpiry) {
		t.Fatal("restart or replay renewed the finite lifetime", e, err)
	}
	// Expiry, rather than a test-written completion or explicit delete request,
	// must create the durable cleanup operation after reopening controller state.
	for time.Now().Before(acceptedExpiry.Add(50 * time.Millisecond)) {
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	finished := make(chan struct{})
	go func() { defer close(finished); a.reconcileTemporaryEnvironments(ctx) }()
	pumpUntil(func() bool {
		select {
		case <-finished:
			return true
		default:
			return false
		}
	})
	e, err = data.GetTemporaryEnvironment(ctx, e.ID)
	if err != nil || e.State != "closed" || e.CleanupJobID == "" || e.CleanupOperationID == "" {
		t.Fatal("expired environment did not finish durable cleanup", e, err)
	}
	cleanupJob, err := data.GetRuntimeJob(ctx, e.CleanupJobID)
	if err != nil || cleanupJob.State != "succeeded" || cleanupJob.Operation != "destroy" || cleanupJob.AppID != e.AppID || cleanupJob.Attempt != 1 {
		t.Fatal("cleanup did not use one owned runtime operation", cleanupJob, err)
	}
	if got := docker("ps", "-aq", "--filter", "label=dispatch.app="+e.AppID); got != "" {
		t.Fatal("expired workload remains", got)
	}
	if got := docker("network", "ls", "-q", "--filter", "label=dispatch.app="+e.AppID); got != "" {
		t.Fatal("owned runtime network remains", got)
	}
	if _, err = data.GetApplicationRoute(ctx, e.AppID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("controller retained a live route after cleanup", err)
	}
	if _, err = (&routing.FilePublisher{Directory: routeDirectory}).Read(ctx, e.AppID); err == nil {
		t.Fatal("runtime retained route publication after cleanup")
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		response, err := publicClient.Get("http://" + route.Hostname)
		if err != nil {
			t.Fatal("cleanup proxy became unavailable", err)
		}
		response.Body.Close()
		if response.StatusCode == http.StatusNotFound && response.Header.Get("X-Dispatch-Deployment") == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Traefik still serves the expired environment", response.StatusCode)
		}
		time.Sleep(50 * time.Millisecond)
	}
	closedApp, err := data.GetApp(ctx, e.AppID)
	if err != nil || closedApp.State != "closed" {
		t.Fatal("generated application history was removed or left open", closedApp, err)
	}
	volume, err = data.GetStorage(ctx, retainedID)
	if err != nil || volume.State != "present" || volume.Identity != beforeIdentity || volume.Policy != "retain" || volume.OwnerID != e.AppID {
		t.Fatal("expiry changed retained data ownership", volume, err)
	}
	if docker("run", "--rm", "--mount", "type=volume,source="+ownedVolume+",target=/data,readonly", "busybox:1.37", "cat", "/data/sentinel") != "retained-data-survives" {
		t.Fatal("expiry removed retained volume data")
	}
	if docker("exec", sharedContainer, "cat", "/www/index.html") != "shared-data-survives" || docker("inspect", "--format", "{{.State.Running}}", sharedContainer) != "true" {
		t.Fatal("expiry changed the shared workload or its data")
	}
	if _, err = data.GetService(ctx, shared.ID); err != nil {
		t.Fatal("shared service registration was removed", err)
	}
	consumers, err := data.ListServiceConsumers(ctx, shared.ID)
	if err != nil || len(consumers) != 0 {
		t.Fatal("closed environment still consumes the shared service", consumers, err)
	}
	if _, err = data.GetServer(ctx, target.ID); err != nil {
		t.Fatal("shared target was removed", err)
	}
	history, err := data.GetDeployment(ctx, e.DeploymentID)
	if err != nil || history.State != core.DeploymentSucceeded || history.CommitSHA != sourceSHA || history.Health.State != "passed" {
		t.Fatal("expiry rewrote accepted deployment history", history, err)
	}
	frozen, err := data.GetDeploymentServiceBindings(ctx, e.DeploymentID)
	if err != nil || len(frozen) != 1 || frozen[0].Service.Fields["token"].EncryptedValue == "" || frozen[0].Service.Fields["token"].Value != "" {
		t.Fatal("expiry discarded encrypted captured service history", err)
	}
	quota = serviceRequestTest(t, a, "GET", "/api/v1/projects/"+project.ID+"/infrastructure/quota", nil, 200)
	if !bytes.Contains(quota, []byte(`"temporaryEnvironments":0`)) {
		t.Fatal("cleanup did not release environment quota", string(quota))
	}
	for range 2 {
		a.reconcileTemporaryEnvironments(ctx)
		out = temporaryKeyed(a, "/api/v1/temporary-environments", "actual-docker-environment", accept)
		if out.Code != 202 || decodeMutation(t, out).OperationID != receipt.OperationID {
			t.Fatal("closed environment lost original creation result")
		}
	}
	if out = request("GET", "next", nil); out.Code != 204 {
		t.Fatal("closed environment dispatched new runtime work", out.Code, out.Body.String())
	}
	out = request("POST", deployed.ID+"/complete", remoteruntime.Completion{LeaseToken: deployed.LeaseToken, Result: executed})
	if out.Code != 422 {
		t.Fatal("old deployment completion was accepted after expiry cleanup", out.Code)
	}
	e, err = data.GetTemporaryEnvironment(ctx, e.ID)
	if err != nil || e.State != "closed" {
		t.Fatal("late deployment completion revived the environment", e, err)
	}
	t.Logf("real Docker environment %s used pinned source %s; controller/worker restart retained workload %s; expiry removed owned route and workload while preserving shared service, volume data and history", e.ID, sourceSHA, containerID)
}
