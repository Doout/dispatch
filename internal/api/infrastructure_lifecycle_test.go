package api

import (
	"context"
	"encoding/json"
	"github.com/doout/dispatch/internal/agentruntime"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/oklog/ulid/v2"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
	"github.com/doout/dispatch/internal/provision"
)

func TestInfrastructureLifecycleAPIReviewedCreationAndHistory(t *testing.T) {
	testInfrastructureLifecycleAPI(t, false)
}
func TestInfrastructureManagedRuntimeIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set DISPATCH_RUNTIME_INTEGRATION=1 to deploy on the enrolled mock target")
	}
	testInfrastructureLifecycleAPI(t, true)
}
func testInfrastructureLifecycleAPI(t *testing.T, realRuntime bool) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	adapter, _ := mock.New(mock.Options{})
	upstream := httptest.NewServer(provider.Handler(adapter, ""))
	defer upstream.Close()
	p, err := a.infrastructureManager().Register(ctx, provision.Registration{Name: "Mock", Endpoint: upstream.URL, Enabled: true, Capabilities: []string{provider.CapabilityCreate, provider.CapabilityInspect, provider.CapabilityDelete}})
	if err != nil {
		t.Fatal(err)
	}
	projects, err := a.store.ListProjects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatal("project fixture", err)
	}
	if err = a.store.CreateSecret(ctx, core.Secret{ID: "lifecycle-ssh", Name: "SSH", Type: core.SecretTypeSSHPrivateKey, Source: core.SecretSourceLocal, PublicValue: "ssh-ed25519 public-fixture", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	catalog := serviceRequestTest(t, a, "GET", "/api/v1/projects/"+projects[0].ID+"/infrastructure/providers", nil, 200)
	if strings.Contains(string(catalog), upstream.URL) || strings.Contains(string(catalog), "credentialSecretId") {
		t.Fatal("catalog leaked registration connection")
	}
	input := provision.CreateInput{ProjectID: projects[0].ID, ProviderID: p.ID, Name: "Reviewed", Region: "mock-region", Size: "mock-small", Image: "mock-linux", Network: "mock-private", SSHKeySecretID: "lifecycle-ssh", Config: map[string]any{}}
	raw := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers/review", input, 201)
	var review core.InfrastructureReview
	if err = json.Unmarshal(raw, &review); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "encryptedRequest") || strings.Contains(string(raw), "public-fixture") {
		t.Fatal("private review internals escaped")
	}
	acceptance := provision.Acceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: "wrong", RequestKey: "stable-api-key"}
	serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers", acceptance, 409)
	acceptance.ConfirmName = input.Name
	raw = serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers", acceptance, 202)
	var accepted provision.Accepted
	if err = json.Unmarshal(raw, &accepted); err != nil {
		t.Fatal(err)
	}
	duplicate := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers", acceptance, 202)
	var repeated provision.Accepted
	_ = json.Unmarshal(duplicate, &repeated)
	if repeated.Operation.ID != accepted.Operation.ID {
		t.Fatal("duplicate request scheduled a different provider operation")
	}
	acceptance.RequestKey = "another-api-key"
	serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers", acceptance, 409)
	manager := a.infrastructureManager()
	now := time.Now().UTC()
	manager.Now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		now = now.Add(3 * time.Second)
		if _, err = manager.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	raw = serviceRequestTest(t, a, "GET", "/api/v1/infrastructure/servers", nil, 200)
	if !strings.Contains(string(raw), `"allocationState":"allocated"`) || strings.Contains(string(raw), `"runtimeState":"ready"`) {
		t.Fatalf("allocation readiness incorrect: %s", raw)
	}
	history := serviceRequestTest(t, a, "GET", "/api/v1/infrastructure/servers/"+accepted.Server.ID+"/operations", nil, 200)
	if !strings.Contains(string(history), `"state":"succeeded"`) || strings.Contains(string(history), "requestDigest") {
		t.Fatalf("unsafe/missing operation history: %s", history)
	}
	raw = serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers/"+accepted.Server.ID+"/enrollment", nil, 201)
	if realRuntime {
		var enrollment provision.Enrollment
		if err = json.Unmarshal(raw, &enrollment); err != nil {
			t.Fatal(err)
		}
		testManagedRuntime(t, a, accepted.Server.ID, enrollment)
	}
}

func testManagedRuntime(t *testing.T, a *API, id string, enrollment provision.Enrollment) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	node, err := a.store.GetPrivateNetwork(ctx, enrollment.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	node.EnrollmentToken = enrollment.Token
	session := enrollNodeTest(t, a, node)
	path := "/api/v1/edge/nodes/" + node.ID + "/runtime/jobs/"
	runtimeNodeRequest(t, a, "GET", path+"next", session.Token, nil, 204)
	if err = a.infrastructureManager().RefreshReadiness(ctx); err != nil {
		t.Fatal(err)
	}
	target, err := a.store.GetServer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	appID := "managed-integration-" + strings.ToLower(ulid.Make().String())
	app := core.App{ID: appID, ProjectID: target.ProjectID, ServerID: target.ID, Name: "Managed integration", BuildType: core.BuildTypeCompose, ComposeContent: "services:\n  app:\n    image: busybox:1.37\n    command: ['sh', '-c', 'echo managed-runtime-ready; sleep 300']\n", CreatedAt: time.Now()}
	if err = a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, kind := range []string{"container", "network"} {
			args := []string{"ps", "-aq", "--filter", "label=dispatch.app=" + appID}
			if kind == "network" {
				args = []string{"network", "ls", "-q", "--filter", "label=dispatch.app=" + appID}
			}
			raw, _ := exec.Command("docker", args...).Output()
			for _, resource := range strings.Fields(string(raw)) {
				args = []string{"rm", "-f", resource}
				if kind == "network" {
					args = []string{"network", "rm", resource}
				}
				_ = exec.Command("docker", args...).Run()
			}
		}
	})
	directory := filepath.Join(t.TempDir(), "runtime")
	worker, err := agentruntime.Open(directory, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { worker.Close() }()
	deployment := core.Deployment{ID: ulid.Make().String(), AppID: app.ID, CommitSHA: "inline", SpecDigest: app.SpecDigest(), Snapshot: core.DeploymentSnapshot{TargetID: target.ID}}
	run := func(op runtimecontract.Operation, d core.Deployment) (remoteruntime.Result, remoteruntime.LeasedJob) {
		t.Helper()
		job, err := a.runtimeBroker().Submit(ctx, ulid.Make().String(), remoteruntime.NewRequest(op, d, app, target))
		if err != nil {
			t.Fatal(err)
		}
		raw := runtimeNodeRequest(t, a, "GET", path+"next", session.Token, nil, 200)
		var leased remoteruntime.LeasedJob
		if err = json.Unmarshal(raw, &leased); err != nil {
			t.Fatal(err)
		}
		result := worker.Run(ctx, leased, nil)
		if result.State != "succeeded" {
			t.Fatalf("%s: %#v", op, result)
		}
		runtimeNodeRequest(t, a, "POST", path+job.ID+"/complete", session.Token, remoteruntime.Completion{LeaseToken: leased.LeaseToken, Result: result}, 204)
		return result, leased
	}
	deployed, leased := run(runtimecontract.Deploy, deployment)
	if len(deployed.Resources) != 1 {
		t.Fatal("deployment did not create one container", deployed)
	}
	if err = worker.Close(); err != nil {
		t.Fatal(err)
	}
	worker, err = agentruntime.Open(directory, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	replayed := worker.Run(ctx, leased, nil)
	if replayed.State != "succeeded" || len(replayed.Resources) != 1 || replayed.Resources[0].ID != deployed.Resources[0].ID {
		t.Fatal("agent restart repeated managed deployment", replayed)
	}
	logs, _ := run(runtimecontract.Logs, core.Deployment{})
	if !strings.Contains(logs.Logs, "managed-runtime-ready") {
		t.Fatal("managed target logs unavailable", logs)
	}
	run(runtimecontract.Destroy, core.Deployment{})
}
