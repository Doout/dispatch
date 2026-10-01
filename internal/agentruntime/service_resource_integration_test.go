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
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/oklog/ulid/v2"
)

func TestServiceResourceAgentRetainedDataAndReplayIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set DISPATCH_RUNTIME_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	id := strings.ToLower(ulid.Make().String())
	name := deploy.ServiceResourceName(id)
	network := "service-recovery-" + id
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", name).Run()
		exec.Command("docker", "volume", "rm", name+"-data").Run()
		exec.Command("docker", "network", "rm", network).Run()
	})
	server := core.Server{ID: "target", AgentNodeID: "node", Address: "agent:node", Runtime: "docker"}
	run := core.ServiceProvisionRun{ID: id, TemplateID: "postgres", ProjectID: "project", ServiceName: "database", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: server.ID, ResourceName: name, Network: network}}
	req, err := deploy.PrepareServiceRequest(core.ServiceProvisionRequest{Run: run, ServiceType: "postgresql", Outputs: []string{"password", "connectionUrl"}})
	if err != nil {
		t.Fatal(err)
	}
	spec := core.DockerServiceProvision{ServerRef: server.ID, Network: network, Image: deploy.DefaultServiceImage}
	directory := filepath.Join(t.TempDir(), "worker")
	w, err := Open(directory, "node")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { w.Close() }()
	execute := func(op runtimecontract.Operation, expected string) (remoteruntime.Result, remoteruntime.LeasedJob) {
		t.Helper()
		request := remoteruntime.NewServiceRequest(req, spec, server)
		request.Operation = op
		request.Service.ExpectedResourceID = expected
		raw, _ := json.Marshal(request)
		digest := sha256.Sum256(raw)
		job := remoteruntime.LeasedJob{ID: ulid.Make().String(), Attempt: 1, Digest: hex.EncodeToString(digest[:]), ExpiresAt: time.Now().Add(2 * time.Minute), Request: request}
		result := w.Run(ctx, job, nil)
		if result.State != "succeeded" {
			t.Fatalf("%s: %s %s", op, result.Code, result.Message)
		}
		return result, job
	}
	first, provision := execute(remoteruntime.ProvisionService, "")
	if first.ServiceOutputs["password"] != req.Password {
		t.Fatal("agent changed accepted credentials")
	}
	query := func(sql string) string {
		t.Helper()
		out, err := exec.Command("docker", "exec", name, "psql", "-U", "dispatch", "-d", "database", "-tAc", sql).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture query failed: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	query("CREATE TABLE recovery_marker(value text); INSERT INTO recovery_marker VALUES ('retained')")
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	w, err = Open(directory, "node")
	if err != nil {
		t.Fatal(err)
	}
	if replay := w.Run(ctx, provision, nil); replay.ServiceOutputs["password"] != req.Password {
		t.Fatal("worker restart lost credential receipt")
	}
	inspection, _ := execute(remoteruntime.ServiceInspect, "")
	if inspection.ServiceResource == nil || inspection.ServiceResource.State != "ready" {
		t.Fatal("typed ownership inspection missing")
	}
	_, deletion := execute(remoteruntime.ServiceDelete, inspection.ServiceResource.ResourceID)
	if err = exec.Command("docker", "volume", "inspect", name+"-data").Run(); err != nil {
		t.Fatal("cleanup removed protected data")
	}
	execute(remoteruntime.ProvisionService, "")
	if query("SELECT value FROM recovery_marker") != "retained" {
		t.Fatal("reprovision lost retained database contents")
	}
	if result := w.Run(ctx, deletion, nil); result.State != "succeeded" {
		t.Fatal("old cleanup receipt was not replayed")
	}
	if query("SELECT value FROM recovery_marker") != "retained" {
		t.Fatal("replayed cleanup removed the replacement container")
	}
}
