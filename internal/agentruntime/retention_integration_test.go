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
	"github.com/oklog/ulid/v2"
)

func TestRemoteRuntimeRetentionIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set DISPATCH_RUNTIME_INTEGRATION=1 for disposable runtime retention resources")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	unique := strings.ToLower(ulid.Make().String())
	app := core.App{ID: "retention-" + unique, ProjectID: "project", ServerID: "server", BuildType: core.BuildTypeDockerfile}
	server := core.Server{ID: "server", AgentNodeID: "node", Runtime: core.ServerRuntimeDocker, Address: "agent:node"}
	deployment := "revision-" + unique
	tag := "dispatch/retention-" + unique + ":old"
	volume := "retention-data-" + unique
	network := "retention-net-" + unique
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "Dockerfile"), []byte("FROM busybox:1.37\nLABEL dispatch.retention-fixture="+unique+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	docker("build", "-q", "-t", tag, workspace)
	image := docker("image", "inspect", "--format", "{{.Id}}", tag)
	docker("volume", "create", volume)
	docker("network", "create", network)
	owned := docker("create", "--label", "dispatch.app="+app.ID, "--label", "dispatch.deployment="+deployment, "--mount", "type=volume,source="+volume+",target=/data", image, "sleep", "600")
	foreign := docker("create", "--label", "dispatch.app=foreign-"+unique, image, "sleep", "600")
	t.Cleanup(func() {
		for _, id := range []string{owned, foreign} {
			_ = exec.Command("docker", "rm", "-f", id).Run()
		}
		_ = exec.Command("docker", "image", "rm", tag).Run()
		_ = exec.Command("docker", "volume", "rm", volume).Run()
		_ = exec.Command("docker", "network", "rm", network).Run()
	})
	dir := filepath.Join(t.TempDir(), "worker")
	w, err := Open(dir, "node")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { w.Close() }()
	at := time.Now().AddDate(0, 0, -30)
	inputs := map[string]any{"projectId": app.ProjectID, "version": 1, "buildType": app.BuildType, "images": map[string]string{"application": image}, "bindings": []core.ServiceRuntimeBinding{{Values: map[string]string{"password": "retention-private-fixture"}}}}
	raw, _ := json.Marshal(inputs)
	artifact := core.RuntimeArtifact{DeploymentID: deployment, AppID: app.ID, ServerID: server.ID, ScopeID: deployment, Metadata: core.RuntimeArtifactMetadata{CreatedAt: at, Images: map[string]string{"application": image}}}
	artifact.Ciphertext, err = w.vault.Encrypt("deployment-runtime:"+deployment+":"+app.ID+":"+server.ID, raw)
	clear(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.SaveRuntimeArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	execute := func(input *remoteruntime.RetentionRequest) (remoteruntime.Result, remoteruntime.LeasedJob) {
		t.Helper()
		r := remoteruntime.NewRetentionRequest(app, server, input)
		raw, _ := json.Marshal(r)
		hash := sha256.Sum256(raw)
		job := remoteruntime.LeasedJob{ID: ulid.Make().String(), Attempt: 1, Digest: hex.EncodeToString(hash[:]), ExpiresAt: time.Now().Add(time.Minute), Request: r}
		out := w.Run(ctx, job, nil)
		if out.State != "succeeded" || out.Retention == nil {
			t.Fatalf("retention operation: %+v", out)
		}
		if err := r.ValidateRetentionResult(out); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(out)
		if strings.Contains(string(encoded), "retention-private-fixture") {
			t.Fatal("retention exposed private inputs")
		}
		return out, job
	}
	inventory, _ := execute(nil)
	var revision core.RuntimeRetentionItem
	for _, item := range inventory.Retention.Items {
		if item.Kind == "revision" {
			revision = item
		}
	}
	if revision.Key == "" || len(revision.Protected) > 0 {
		t.Fatalf("stopped revision unavailable: %+v", inventory)
	}
	review := &remoteruntime.RetentionRequest{ReviewID: "review", Digest: strings.Repeat("a", 64), Item: revision}
	removed, job := execute(review)
	if removed.Retention.Outcome.State != "removed" {
		t.Fatalf("revision not retired %+v", removed)
	}
	a, err := w.GetRuntimeArtifact(ctx, deployment)
	if err != nil || !a.Metadata.Retired || a.Ciphertext != "" {
		t.Fatalf("private inputs survived %+v %v", a, err)
	}
	w.Close()
	w, err = Open(dir, "node")
	if err != nil {
		t.Fatal(err)
	}
	replay := w.Run(ctx, job, nil)
	if replay.Retention == nil || replay.Retention.Outcome.State != "removed" {
		t.Fatalf("durable receipt lost %+v", replay)
	}
	inventory, _ = execute(nil)
	if len(inventory.Retention.Items) != 1 || len(inventory.Retention.Items[0].Protected) == 0 {
		t.Fatalf("shared image unprotected %+v", inventory)
	}
	docker("rm", foreign)
	inventory, _ = execute(nil)
	if len(inventory.Retention.Items) != 1 || len(inventory.Retention.Items[0].Protected) != 0 {
		t.Fatalf("unreferenced image not offered %+v", inventory)
	}
	review.Item = inventory.Retention.Items[0]
	removed, _ = execute(review)
	if removed.Retention.Outcome.State != "removed" {
		t.Fatalf("image not removed %+v", removed)
	}
	absent, _ := execute(review)
	if absent.Retention.Outcome.State != "absent" {
		t.Fatalf("already absent retry failed %+v", absent)
	}
	docker("volume", "inspect", volume)
	docker("network", "inspect", network)
}
