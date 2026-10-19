package workflowrunner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

type ExecutionEvent struct {
	Log    *string `json:"log,omitempty"`
	Result *Result `json:"result,omitempty"`
}

// ContainerExecutor configuration comes exclusively from the worker operator.
// Job payloads cannot change mounts, networking, image or resource limits.
type ContainerExecutor struct {
	Image, Mode, Network     string
	AllowDocker              bool
	MemoryMiB, DiskMiB, PIDs int
	CPUs                     float64
}

var imageDigest = regexp.MustCompile(`(?:^sha256:|@sha256:)[a-f0-9]{64}$`)

func (e ContainerExecutor) Validate() error {
	if !imageDigest.MatchString(e.Image) || (e.Mode != "tenant" && e.Mode != "managed") {
		return errors.New("worker requires a pinned execution image and tenant or managed mode")
	}
	if e.Mode == "managed" && (e.AllowDocker || (e.Network != "" && e.Network != "none")) {
		return errors.New("managed workers cannot mount Docker or use a network")
	}
	if e.MemoryMiB < 64 || e.MemoryMiB > 65536 || e.DiskMiB < 16 || e.DiskMiB > 65536 || e.PIDs < 16 || e.PIDs > 4096 || e.CPUs <= 0 || e.CPUs > 64 || math.IsNaN(e.CPUs) || math.IsInf(e.CPUs, 0) {
		return errors.New("worker resource limits are invalid")
	}
	if e.Network == "host" || e.Network == "container" || strings.HasPrefix(e.Network, "container:") {
		return errors.New("worker containers require an isolated network namespace")
	}
	return nil
}
func (e ContainerExecutor) arguments(name string) []string {
	network := e.Network
	if network == "" {
		network = "none"
		if e.Mode == "tenant" {
			network = "bridge"
		}
	}
	args := []string{"run", "--rm", "--interactive", "--name", name, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", strconv.Itoa(e.PIDs), "--memory", fmt.Sprintf("%dm", e.MemoryMiB), "--memory-swap", fmt.Sprintf("%dm", e.MemoryMiB), "--cpus", strconv.FormatFloat(e.CPUs, 'f', 2, 64), "--network", network, "--user", "65532:65532", "--workdir", "/work", "--tmpfs", fmt.Sprintf("/work:rw,size=%dm,mode=0700,uid=65532,gid=65532", e.DiskMiB), "--tmpfs", "/tmp:rw,size=64m,mode=1777", "--env", "HOME=/work", "--env", "TMPDIR=/work", "--entrypoint", "/usr/local/bin/dispatch-worker"}
	if e.AllowDocker {
		args = append(args, "--mount", "type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock")
		if info, err := os.Stat("/var/run/docker.sock"); err == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				args = append(args, "--group-add", strconv.FormatUint(uint64(stat.Gid), 10))
			}
		}
	}
	return append(args, e.Image, "execute")
}
func (e ContainerExecutor) Execute(ctx context.Context, r Request, _ string, progress func(string)) Result {
	fail := func(err error) Result {
		state := "failed"
		if ctx.Err() != nil {
			state = "cancelled"
		}
		return Result{State: state, Error: r.Redact(err.Error())}
	}
	if err := e.Validate(); err != nil {
		return fail(err)
	}
	if r.Mode != e.Mode {
		return fail(errors.New("worker execution mode does not match the request"))
	}
	if err := r.Validate(); err != nil {
		return fail(err)
	}
	if r.Deployment != nil && r.Deployment.Server.Runtime == core.ServerRuntimeDocker && !e.AllowDocker {
		return fail(errors.New("this worker is not configured for Docker deployment"))
	}
	if r.Workflow != nil {
		var job struct {
			Builder string `json:"builder"`
		}
		if err := json.Unmarshal(r.Workflow.Job, &job); err != nil {
			return fail(err)
		}
		if job.Builder != "" && !e.AllowDocker {
			return fail(errors.New("this worker is not configured for Docker builds"))
		}
	}
	name := "dispatch-job-" + strings.ToLower(ulid.Make().String())
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "docker", "rm", "--force", name).Run()
	}()
	raw, err := json.Marshal(r)
	if err != nil {
		return fail(err)
	}
	defer clear(raw)
	command := exec.CommandContext(ctx, "docker", e.arguments(name)...)
	command.Stdin = bytes.NewReader(raw)
	// stderr only contains Docker transport diagnostics, never job stdout.
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	if err = command.Start(); err != nil {
		return fail(err)
	}
	var result *Result
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), MaxPayload+MaxLog)
	for scanner.Scan() {
		var event ExecutionEvent
		if err = json.Unmarshal(scanner.Bytes(), &event); err != nil {
			_ = command.Process.Kill()
			break
		}
		if event.Log != nil && progress != nil {
			progress(r.Redact(*event.Log))
		}
		if event.Result != nil {
			result = event.Result
		}
	}
	scanErr := scanner.Err()
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	if err != nil {
		return fail(errors.New("worker container returned an invalid execution event"))
	}
	if scanErr != nil {
		return fail(scanErr)
	}
	if waitErr != nil {
		return fail(fmt.Errorf("worker container failed: %w", waitErr))
	}
	if result == nil {
		return fail(errors.New("worker container ended without a result"))
	}
	return *result
}
