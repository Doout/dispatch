package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
)

type dockerEngine struct {
	executor deploy.DockerExecutor
	command  func(context.Context, ...string) (string, error)
}

type boundedOutput struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(b.data)+n > b.limit {
		return 0, errors.New("runtime output exceeds its limit")
	}
	b.data = append(b.data, p...)
	return n, nil
}
func command(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	output := &boundedOutput{limit: remoteruntime.MaxResult / 2}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	return string(output.data), err
}

var containerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (e dockerEngine) resources(ctx context.Context, request remoteruntime.Request) ([]remoteruntime.Resource, error) {
	output, err := e.command(ctx, "ps", "--all", "--no-trunc", "--filter", "label=dispatch.app="+request.Application.ID, "--format", "{{.ID}}")
	if err != nil {
		return nil, errors.New("Cannot list the workload's containers.")
	}
	ids := strings.Fields(output)
	if len(ids) > 1000 {
		return nil, errors.New("The workload exceeds the container limit.")
	}
	resources := make([]remoteruntime.Resource, 0, len(ids))
	for _, id := range ids {
		if !containerID.MatchString(id) {
			return nil, errors.New("Docker returned an invalid container identity.")
		}
		resource, err := e.inspect(ctx, id, request.Application.ID)
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

func (e dockerEngine) inspect(ctx context.Context, id, app string) (remoteruntime.Resource, error) {
	output, err := e.command(ctx, "inspect", "--format", `{"id":{{json .Id}},"name":{{json .Name}},"image":{{json .Image}},"state":{{json .State.Status}},"applicationId":{{json (index .Config.Labels "dispatch.app")}},"deploymentId":{{json (index .Config.Labels "dispatch.deployment")}}}`, id)
	var resource remoteruntime.Resource
	if err != nil || json.Unmarshal([]byte(output), &resource) != nil || resource.ID != id || resource.ApplicationID != app {
		return resource, &runtimecontract.Error{Code: runtimecontract.OwnershipConflict, Message: "Container ownership changed; inspect the workload before retrying."}
	}
	return resource, nil
}

func (e dockerEngine) Execute(ctx context.Context, request remoteruntime.Request, progress deploy.Progress) remoteruntime.Result {
	server, app := request.Server, request.App()
	// Target identity remains stable; Docker commands always address this agent's
	// local daemon. No request may supply a shell command or a remote Docker URL.
	server.Address, server.AgentNodeID = "local", ""
	deployment := request.Deployment
	deployment.Snapshot = request.Snapshot
	var err error
	var result remoteruntime.Result
	switch request.Operation {
	case remoteruntime.RetentionInspect:
		var items []core.RuntimeRetentionItem
		items, err = e.executor.InspectRetention(ctx, app, server)
		if err == nil {
			result.Retention = &remoteruntime.RetentionResult{Items: items}
		}
	case remoteruntime.RetentionPrune:
		var outcome core.RuntimeRetentionOutcome
		outcome, err = e.executor.PruneRetention(ctx, app, server, request.Retention.Item)
		result.Retention = &remoteruntime.RetentionResult{Outcome: &outcome}
	case remoteruntime.StorageInspect, remoteruntime.StorageDelete:
		backend := deploy.RuntimeStorage{Run: func(ctx context.Context, _ io.Reader, out io.Writer, name string, args ...string) error {
			if name != "docker" {
				return errors.New("unsupported storage command")
			}
			value, err := e.command(ctx, args...)
			_, _ = io.WriteString(out, value)
			return err
		}}
		if request.Operation == remoteruntime.StorageInspect {
			result.Storage, err = backend.Inspect(ctx, server)
		} else {
			err = backend.Delete(ctx, server, *request.Storage)
		}
	case remoteruntime.ServiceInspect:
		var inspected core.ServiceResourceInspection
		inspected, err = e.executor.InspectServiceResource(ctx, request.Service.Request, server)
		if err == nil {
			result.ServiceResource = &inspected
		}
	case remoteruntime.ServiceDelete:
		err = e.executor.DeleteServiceResource(ctx, request.Service.Request, server, request.Service.ExpectedResourceID)
	case remoteruntime.ProvisionService:
		if request.Service.Request.Password != "" {
			result.ServiceOutputs, err = e.executor.Provision(ctx, request.Service.Request, request.Service.Docker, server)
			break
		}
		name := deploy.ServiceResourceName(request.Service.Request.Run.ID)
		var existing string
		existing, err = e.command(ctx, "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
		if err == nil && strings.TrimSpace(existing) != "" {
			err = &runtimecontract.Error{Code: runtimecontract.OwnershipConflict, Message: "A service container already exists for this operation; inspect it before recovery."}
		}
		if err == nil {
			existing, err = e.command(ctx, "volume", "ls", "--filter", "name=^"+name+"-data$", "--format", "{{.Name}}")
			if err == nil && strings.TrimSpace(existing) != "" {
				err = &runtimecontract.Error{Code: runtimecontract.OwnershipConflict, Message: "Retained service data already exists; restore its original credentials before recovery."}
			}
		}
		if err == nil {
			result.ServiceOutputs, err = e.executor.Provision(ctx, request.Service.Request, request.Service.Docker, server)
		}
	case runtimecontract.Deploy:
		err = e.executor.Deploy(ctx, deployment, app, server, progress)
	case runtimecontract.Rollback:
		var current string
		current, err = e.executor.CurrentRuntimeIdentity(ctx, app, server)
		if err == nil && current != request.ExpectedRuntime {
			err = &runtimecontract.Error{Code: runtimecontract.Conflict, Message: "The remote workload changed after rollback review."}
		}
		if err == nil {
			err = e.executor.RollbackRuntime(ctx, deployment, core.Deployment{ID: request.SourceDeploymentID, AppID: app.ID, Snapshot: core.DeploymentSnapshot{TargetID: server.ID}}, app, server, progress)
		}
	case runtimecontract.Destroy:
		err = e.executor.Cleanup(ctx, app, server, progress)
	case runtimecontract.Inspect, runtimecontract.Start, runtimecontract.Stop, runtimecontract.Logs:
		if request.Operation == runtimecontract.Inspect {
			result.RuntimeDigest, err = e.executor.CurrentRuntimeIdentity(ctx, app, server)
			if err != nil {
				break
			}
			if request.SourceDeploymentID != "" {
				var preview deploy.RollbackPreview
				preview, err = e.executor.PreviewRuntimeRollback(ctx, core.Deployment{ID: request.SourceDeploymentID, AppID: app.ID, Snapshot: core.DeploymentSnapshot{TargetID: server.ID}}, app, server)
				if err != nil {
					break
				}
				result.Rollback = &preview
			}
		}
		result.Resources, err = e.resources(ctx, request)
		if err != nil {
			break
		}
		var logs strings.Builder
		for _, resource := range result.Resources {
			if _, err = e.inspect(ctx, resource.ID, app.ID); err != nil {
				break
			}
			switch request.Operation {
			case runtimecontract.Start:
				_, err = e.command(ctx, "start", resource.ID)
			case runtimecontract.Stop:
				_, err = e.command(ctx, "stop", "--time", "20", resource.ID)
			case runtimecontract.Logs:
				var output string
				output, err = e.command(ctx, "logs", "--tail", strconv.Itoa(request.LogLimit), "--timestamps", resource.ID)
				if logs.Len()+len(output)+len(resource.Name) > remoteruntime.MaxResult/2 {
					err = errors.New("Workload logs exceed the response limit.")
				} else {
					_, _ = io.WriteString(&logs, resource.Name+"\n"+output)
				}
			}
			if err != nil {
				break
			}
		}
		result.Logs = logs.String()
		if err == nil && (request.Operation == runtimecontract.Start || request.Operation == runtimecontract.Stop) {
			result.Resources, err = e.resources(ctx, request)
		}
	default:
		return failure(runtimecontract.Unsupported, "This agent does not implement the requested operation.")
	}
	mutating := request.Operation != runtimecontract.Inspect && request.Operation != runtimecontract.Logs && request.Operation != remoteruntime.StorageInspect && request.Operation != remoteruntime.RetentionInspect
	// Stopping the Docker CLI does not prove that the daemon stopped its
	// mutation. Keep the application locked until a later inspection reconciles
	// the outcome, including when the CLI returns an ordinary killed-process error.
	if mutating && ctx.Err() != nil {
		return failure(runtimecontract.Uncertain, "Runtime execution was interrupted; inspect the workload before retrying.")
	}
	if err != nil {
		var classified *runtimecontract.Error
		if !errors.As(runtimecontract.Classify(request.Operation, err), &classified) {
			return failure(runtimecontract.Failed, "The runtime operation failed.")
		}
		if mutating && (classified.Code == runtimecontract.Cancelled || classified.Code == runtimecontract.DeadlineExceeded) {
			return failure(runtimecontract.Uncertain, "Runtime execution was interrupted; inspect the workload before retrying.")
		}
		failed := failure(classified.Code, request.Redact(classified.Message))
		failed.Retention = result.Retention
		return failed
	}
	if request.Operation == runtimecontract.Deploy || request.Operation == runtimecontract.Destroy || request.Operation == runtimecontract.Rollback {
		result.Resources, err = e.resources(ctx, request)
		if err != nil {
			return failure(runtimecontract.Uncertain, "The operation completed but the resulting workload cannot be inspected.")
		}
	}
	result.State = "succeeded"
	return result
}
