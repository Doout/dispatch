package agentruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/oklog/ulid/v2"
)

func TestRemoteStorageLifecycleIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set DISPATCH_RUNTIME_INTEGRATION=1 to exercise target storage")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	name := "dispatch-storage-test-" + strings.ToLower(ulid.Make().String())
	docker := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("docker %s: %v %s", args[0], err, out)
		}
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
		_ = exec.Command("docker", "volume", "rm", name).Run()
	})
	createVolume := func() {
		docker("volume", "create", "--label", "dispatch.app="+name, "--label", "unrelated.secret=never-export-this-label", name)
	}
	createVolume()
	docker("create", "--name", name, "--mount", "type=volume,source="+name+",target=/data", "busybox:1.37", "sleep", "600")
	server := core.Server{ID: "server", Runtime: core.ServerRuntimeDocker, AgentNodeID: "node", Address: "agent:node"}
	w, err := Open(filepath.Join(t.TempDir(), "runtime"), "node")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	run := func(request remoteruntime.Request) (remoteruntime.Result, remoteruntime.LeasedJob) {
		t.Helper()
		job := withRequest(leasedJob(), request)
		job.ID = ulid.Make().String()
		job.ExpiresAt = time.Now().Add(time.Minute)
		return w.Run(ctx, job, nil), job
	}
	inspect := func() core.StorageResource {
		t.Helper()
		result, _ := run(remoteruntime.NewStorageRequest(server, nil))
		if result.State != "succeeded" {
			t.Fatalf("inspect: %s %s", result.State, result.Message)
		}
		for _, v := range result.Storage {
			if v.Resource.Name == name {
				if v.Labels["unrelated.secret"] != "" {
					t.Fatal("arbitrary volume label escaped target")
				}
				item := v.Resource
				item.ID = deploy.StorageID(server.ID, item.Kind, "", item.Name)
				item.ServerID = server.ID
				item.ProjectID = "project"
				item.OwnerKind = "application"
				item.OwnerID = name
				item.Ownership = "verified"
				item.State = "present"
				item.Policy = "destroy"
				item.Revision = 1
				return item
			}
		}
		t.Fatal("target volume missing")
		return core.StorageResource{}
	}
	item := inspect()
	if len(item.Consumers) != 1 || item.Consumers[0].Active {
		t.Fatalf("stopped consumer missing: %#v", item.Consumers)
	}
	// A stale empty consumer list does not authorize data deletion on the target.
	item.Consumers = nil
	result, _ := run(remoteruntime.NewStorageRequest(server, &item))
	if result.State == "succeeded" {
		t.Fatal("deleted data mounted by a stopped container")
	}
	docker("volume", "inspect", name)
	docker("rm", name)
	item = inspect()
	item.Policy = "retain"
	result, _ = run(remoteruntime.NewStorageRequest(server, &item))
	if result.State == "succeeded" {
		t.Fatal("retained data deleted")
	}
	item.Policy = "destroy"
	result, deletion := run(remoteruntime.NewStorageRequest(server, &item))
	if result.State != "succeeded" {
		t.Fatalf("reviewed deletion failed: %s %s", result.State, result.Message)
	}
	if exec.CommandContext(ctx, "docker", "volume", "inspect", name).Run() == nil {
		t.Fatal("volume survived confirmed deletion")
	}
	createVolume()
	directory := w.directory
	w.Close()
	w, err = Open(directory, "node")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	replay := w.Run(ctx, deletion, nil)
	if replay.State != "succeeded" {
		t.Fatalf("lost deletion receipt: %#v", replay)
	}
	// Reusing a name cannot turn replay of a completed deletion into another effect.
	docker("volume", "inspect", name)
}
