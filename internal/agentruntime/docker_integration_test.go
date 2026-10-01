package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/oklog/ulid/v2"
)

func TestRemoteDockerLifecycleIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set DISPATCH_RUNTIME_INTEGRATION=1 to exercise the Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	appID := "agent-test-" + strings.ToLower(ulid.Make().String())
	app := core.App{ID: appID, ProjectID: "project", ServerID: "server", Name: "Remote integration", BuildType: core.BuildTypeCompose, ComposeContent: "services:\n  app:\n    image: busybox:1.37\n    command: ['sh', '-c', 'echo runtime-ready; sleep 600']\n"}
	server := core.Server{ID: "server", AgentNodeID: "node", Runtime: core.ServerRuntimeDocker, Address: "agent:node"}
	w, err := Open(filepath.Join(t.TempDir(), "runtime"), "node")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
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
	execute := func(op runtimecontract.Operation, d core.Deployment, source, expected string) (remoteruntime.Result, remoteruntime.LeasedJob) {
		t.Helper()
		r := remoteruntime.NewRequest(op, d, app, server)
		r.SourceDeploymentID, r.ExpectedRuntime = source, expected
		raw, _ := json.Marshal(r)
		digest := sha256.Sum256(raw)
		job := remoteruntime.LeasedJob{ID: ulid.Make().String(), Attempt: 1, Digest: hex.EncodeToString(digest[:]), ExpiresAt: time.Now().Add(2 * time.Minute), Request: r}
		result := w.Run(ctx, job, nil)
		if result.State != "succeeded" {
			t.Fatalf("%s: %#v", op, result)
		}
		return result, job
	}
	first := core.Deployment{ID: ulid.Make().String(), AppID: app.ID, CommitSHA: "inline", SpecDigest: app.SpecDigest(), Snapshot: core.DeploymentSnapshot{TargetID: server.ID}}
	deployed, original := execute(runtimecontract.Deploy, first, "", "")
	if len(deployed.Resources) != 1 {
		t.Fatalf("unexpected workload %#v", deployed)
	}
	container := deployed.Resources[0].ID
	dir := w.directory
	w.Close()
	w, err = Open(dir, "node")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	replay := w.Run(ctx, original, nil)
	if len(replay.Resources) != 1 || replay.Resources[0].ID != container {
		t.Fatal("replay replaced the original container")
	}
	logs, _ := execute(runtimecontract.Logs, core.Deployment{}, "", "")
	if !strings.Contains(logs.Logs, "runtime-ready") {
		t.Fatalf("missing bounded logs: %s", logs.Logs)
	}
	stopped, _ := execute(runtimecontract.Stop, core.Deployment{}, "", "")
	if stopped.Resources[0].State != "exited" {
		t.Fatal("stop did not stop workload")
	}
	started, _ := execute(runtimecontract.Start, core.Deployment{}, "", "")
	if started.Resources[0].State != "running" {
		t.Fatal("start did not start workload")
	}
	review, _ := execute(runtimecontract.Inspect, core.Deployment{}, first.ID, "")
	if review.Rollback == nil || !review.Rollback.Available || review.RuntimeDigest == "" {
		t.Fatal("missing retained runtime evidence")
	}
	restored := core.Deployment{ID: ulid.Make().String(), AppID: app.ID, CommitSHA: "inline", SpecDigest: app.SpecDigest(), Snapshot: core.DeploymentSnapshot{TargetID: server.ID}}
	rollback, _ := execute(runtimecontract.Rollback, restored, first.ID, review.RuntimeDigest)
	if rollback.Resources[0].Image != deployed.Resources[0].Image {
		t.Fatal("rollback rebuilt the image")
	}
	destroyed, _ := execute(runtimecontract.Destroy, core.Deployment{}, "", "")
	if len(destroyed.Resources) != 0 {
		t.Fatal("destroy left owned workloads")
	}
	execute(runtimecontract.Destroy, core.Deployment{}, "", "")
}

func TestRemotePostgresProvisionIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set DISPATCH_RUNTIME_INTEGRATION=1 to exercise Docker service provisioning")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	runID := strings.ToLower(ulid.Make().String())
	name := "dispatch-svc-" + runID
	network := "dispatch-service-test-" + runID
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
		_ = exec.Command("docker", "volume", "rm", name+"-data").Run()
		_ = exec.Command("docker", "network", "rm", network).Run()
	})
	w, err := Open(filepath.Join(t.TempDir(), "runtime"), "node")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	server := core.Server{ID: "server", AgentNodeID: "node", Runtime: core.ServerRuntimeDocker, Address: "agent:node"}
	run := core.ServiceProvisionRun{ID: runID, TemplateID: "postgres", ProjectID: "project", ServiceName: "database", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: server.ID, Network: network}}
	request := remoteruntime.NewServiceRequest(core.ServiceProvisionRequest{Run: run, ServiceType: "postgresql", Outputs: []string{"host", "port", "database", "username", "password", "connectionUrl"}}, core.DockerServiceProvision{ServerRef: server.ID, Image: "postgres:17-bookworm", Network: network}, server)
	raw, _ := json.Marshal(request)
	hash := sha256.Sum256(raw)
	job := remoteruntime.LeasedJob{ID: "service-" + runID, Attempt: 1, Digest: hex.EncodeToString(hash[:]), ExpiresAt: time.Now().Add(90 * time.Second), Request: request}
	result := w.Run(ctx, job, nil)
	if result.State != "succeeded" || result.ServiceOutputs["password"] == "" {
		t.Fatalf("service provision failed: %s %s", result.State, result.Message)
	}
	before, err := exec.Command("docker", "inspect", "--format", "{{.Id}}", name).Output()
	if err != nil {
		t.Fatal(err)
	}
	replay := w.Run(ctx, job, nil)
	if replay.ServiceOutputs["password"] != result.ServiceOutputs["password"] {
		t.Fatal("replay rotated provisioned credentials")
	}
	after, err := exec.Command("docker", "inspect", "--format", "{{.Id}}", name).Output()
	if err != nil || string(before) != string(after) {
		t.Fatal("replay replaced database")
	}
	if err = exec.Command("docker", "volume", "inspect", name+"-data").Run(); err != nil {
		t.Fatal("persistent database volume missing")
	}
	// A fresh request for an existing operation's resources may not adopt its data
	// with newly generated credentials, even if its container has disappeared.
	if err = exec.Command("docker", "rm", "-f", name).Run(); err != nil {
		t.Fatal(err)
	}
	job.ID = "recovery-" + runID
	result = w.Run(ctx, job, nil)
	if result.Code != runtimecontract.OwnershipConflict {
		t.Fatalf("retained data was reused: %s", result.Code)
	}
	if err = exec.Command("docker", "volume", "inspect", name+"-data").Run(); err != nil {
		t.Fatal("failed recovery removed retained database volume")
	}
}
