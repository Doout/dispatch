package workflowrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

func TestDisposableManagedWorkerIntegration(t *testing.T) {
	image := os.Getenv("DISPATCH_WORKER_TEST_IMAGE")
	if image == "" {
		t.Skip("set DISPATCH_WORKER_TEST_IMAGE to a built, pinned worker image")
	}
	executor := ContainerExecutor{Image: image, Mode: "managed", MemoryMiB: 256, DiskMiB: 64, PIDs: 32, CPUs: 0.5}
	request := testRequest()
	request.Mode = "managed"
	request.Workflow.Secrets = map[string]string{"TOKEN": "integration-worker-private-value"}
	request.Workflow.Job = json.RawMessage(`{"run":"test ! -e /var/run/docker.sock; test ! -e /var/lib/dispatch-worker; test -z \"${DISPATCH_EDGE_TOKEN:-}\"; test -z \"${DISPATCH_MASTER_KEY_FILE:-}\"; test $(id -u) = 65532; ! touch /forbidden 2>/dev/null; printf 'running\\n'; sleep 2; printf '%s\\n' \"$TOKEN\"; printf 'result=isolated\\n' > \"$DISPATCH_OUTPUT_FILE\"","outputs":["result"]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	live := make(chan string, 8)
	done := make(chan Result, 1)
	go func() {
		done <- executor.Execute(ctx, request, "", func(log string) {
			select {
			case live <- log:
			default:
			}
		})
	}()
	sawLive := false
	for {
		select {
		case log := <-live:
			if strings.Contains(log, request.Workflow.Secrets["TOKEN"]) {
				t.Fatal("unredacted live output")
			}
			if strings.Contains(log, "running") {
				sawLive = true
			}
		case result := <-done:
			if result.State != "succeeded" || result.Outputs["result"] != "isolated" || !sawLive || strings.Contains(result.Log, request.Workflow.Secrets["TOKEN"]) {
				t.Fatalf("managed worker result: %+v live=%v", result, sawLive)
			}
			return
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestDisposableWorkerCancellationIntegration(t *testing.T) {
	image := os.Getenv("DISPATCH_WORKER_TEST_IMAGE")
	if image == "" {
		t.Skip("set DISPATCH_WORKER_TEST_IMAGE to a built, pinned worker image")
	}
	executor := ContainerExecutor{Image: image, Mode: "managed", MemoryMiB: 256, DiskMiB: 64, PIDs: 32, CPUs: 0.5}
	request := testRequest()
	request.Mode = "managed"
	request.Workflow.Job = json.RawMessage(`{"run":"printf 'started\\n'; sleep 120"}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		done <- executor.Execute(ctx, request, "", func(log string) {
			if strings.Contains(log, "started") {
				cancel()
			}
		})
	}()
	select {
	case result := <-done:
		if result.State != "cancelled" {
			t.Fatalf("cancellation result: %+v", result)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

func TestDisposableTenantDockerWorkerIntegration(t *testing.T) {
	image := os.Getenv("DISPATCH_WORKER_TEST_IMAGE")
	if image == "" || os.Getenv("DISPATCH_WORKER_TEST_DOCKER") != "true" {
		t.Skip("set DISPATCH_WORKER_TEST_IMAGE and DISPATCH_WORKER_TEST_DOCKER=true on a disposable Docker host")
	}
	suffix := strings.ToLower(ulid.Make().String())
	builtImage, container := "dispatch-worker-test:"+suffix, "dispatch-worker-test-"+suffix
	docker := func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	}
	// The outer cleanup still runs if the execution container is cancelled before
	// its shell trap can remove resources created through the Docker socket.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = docker(ctx, "rm", "--force", container)
		_, _ = docker(ctx, "image", "rm", "--force", builtImage)
	})
	command := fmt.Sprintf(`image=%s
container=%s
cleanup() {
  docker rm --force "$container" >/dev/null 2>&1 || true
  docker image rm --force "$image" >/dev/null 2>&1 || true
}
trap cleanup EXIT
mkdir -p rootfs/bin
cp /bin/sh /bin/sleep rootfs/bin/
ldd /bin/sh /bin/sleep | awk '/=> \// {print $3} /^[[:space:]]*\// && NF == 2 {print $1}' | sort -u | while read -r library; do
  mkdir -p "rootfs$(dirname "$library")"
  cp "$library" "rootfs$library"
done
printf '%%s\n' '#!/bin/sh' 'printf "tenant-container-live\n"' 'sleep 5' 'printf "tenant-container-finished\n"' > rootfs/app.sh
printf '%%s\n' 'FROM scratch' 'COPY rootfs/ /' 'ENTRYPOINT ["/bin/sh", "/app.sh"]' > Dockerfile
printf 'starting-docker-build\n'
docker build --network=none --pull=false --tag "$image" .
docker run --rm --name "$container" --network=none --read-only --cap-drop=ALL --security-opt=no-new-privileges "$image"
printf 'result=container-ran\n' > "$DISPATCH_OUTPUT_FILE"
`, builtImage, container)
	job, err := json.Marshal(map[string]any{"builder": "docker", "run": command, "outputs": []string{"result"}})
	if err != nil {
		t.Fatal(err)
	}
	request := testRequest()
	request.Mode = "tenant"
	request.Workflow.Job = job
	executor := ContainerExecutor{Image: image, Mode: "tenant", Network: "none", AllowDocker: true, MemoryMiB: 512, DiskMiB: 64, PIDs: 128, CPUs: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sawBuild, checkedLive, sawLive := false, false, false
	var liveErr error
	result := executor.Execute(ctx, request, "", func(log string) {
		sawBuild = sawBuild || strings.Contains(log, "starting-docker-build")
		if !checkedLive && strings.Contains(log, "tenant-container-live") {
			checkedLive = true
			inspectCtx, stop := context.WithTimeout(ctx, 3*time.Second)
			output, err := docker(inspectCtx, "inspect", "--format", "{{.State.Running}}", container)
			stop()
			sawLive = err == nil && strings.TrimSpace(string(output)) == "true"
			if !sawLive {
				liveErr = fmt.Errorf("inspect live container: %v: %s", err, output)
			}
		}
	})
	if result.State != "succeeded" || result.Outputs["result"] != "container-ran" || !sawBuild || !sawLive || !strings.Contains(result.Log, "tenant-container-finished") {
		t.Fatalf("tenant Docker result: %+v build=%v live=%v inspect=%v", result, sawBuild, sawLive, liveErr)
	}
	for _, args := range [][]string{{"container", "ls", "--all", "--quiet", "--filter", "name=" + container}, {"image", "ls", "--quiet", "--filter", "reference=" + builtImage}} {
		if output, err := docker(ctx, args...); err != nil || strings.TrimSpace(string(output)) != "" {
			t.Fatalf("Docker cleanup check failed: %v: %v %s", args, err, output)
		}
	}
}
