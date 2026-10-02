package deploy

import (
	"context"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
)

// CapabilityExecutor describes operations available for a concrete application
// and target. Implementations reject unsupported operations before doing work.
type CapabilityExecutor interface {
	RuntimeCapabilities(core.App, core.Server) runtimecontract.Manifest
}

func executorCapabilities(e Executor, app core.App, server core.Server) runtimecontract.Manifest {
	if declared, ok := e.(CapabilityExecutor); ok {
		return declared.RuntimeCapabilities(app, server)
	}
	operations := []runtimecontract.Operation{}
	if e != nil {
		operations = append(operations, runtimecontract.Deploy)
	}
	if _, ok := e.(CleanupExecutor); ok {
		operations = append(operations, runtimecontract.Destroy)
	}
	return runtimecontract.Describe("custom", "configured", operations...)
}

func (s *Service) RuntimeCapabilities(app core.App, server core.Server) runtimecontract.Manifest {
	return executorCapabilities(s.executor, app, server)
}

func (e RuntimeExecutor) selected(app core.App) Executor {
	if app.BuildType == core.BuildTypeHelm {
		return e.Helm
	}
	return e.Default
}

func (e RuntimeExecutor) RuntimeCapabilities(app core.App, server core.Server) runtimecontract.Manifest {
	return executorCapabilities(e.selected(app), app, server)
}

// Execute is the common dispatcher. Drivers may add typed optional interfaces
// for additional operations; v1 rejects them until they are implemented.
func (e RuntimeExecutor) Execute(ctx context.Context, op runtimecontract.Operation, deployment core.Deployment, app core.App, server core.Server, progress Progress) error {
	if err := e.RuntimeCapabilities(app, server).Check(ctx, op); err != nil {
		return err
	}
	executor := e.selected(app)
	switch op {
	case runtimecontract.Deploy:
		return executor.Deploy(ctx, deployment, app, server, progress)
	case runtimecontract.Destroy:
		return executor.(CleanupExecutor).Cleanup(ctx, app, server, progress)
	default:
		return &runtimecontract.Error{Code: runtimecontract.Unsupported, Action: op, Message: "The runtime operation has no configured implementation."}
	}
}

func (SimulationExecutor) RuntimeCapabilities(core.App, core.Server) runtimecontract.Manifest {
	return runtimecontract.Describe("simulation", "simulation", runtimecontract.Deploy, runtimecontract.Destroy)
}

func (DockerExecutor) RuntimeCapabilities(app core.App, server core.Server) runtimecontract.Manifest {
	if server.Address != "local" && server.Address != "localhost" && server.Address != "127.0.0.1" {
		return runtimecontract.Describe("docker", "remote")
	}
	if app.BuildType != core.BuildTypeDockerfile && app.BuildType != core.BuildTypeCompose {
		return runtimecontract.Describe("docker", "local")
	}
	if server.Runtime != "" && server.Runtime != core.ServerRuntimeDocker {
		return runtimecontract.Describe("docker", "incompatible")
	}
	return runtimecontract.Describe("docker", "local", runtimecontract.Deploy, runtimecontract.Destroy)
}

func (HelmExecutor) RuntimeCapabilities(app core.App, server core.Server) runtimecontract.Manifest {
	if app.BuildType != core.BuildTypeHelm || !core.IsKubernetesRuntime(server.Runtime) {
		return runtimecontract.Describe("helm", "incompatible")
	}
	// Retained Helm rollback is managed by the deployment service; it is not
	// advertised as an operation on this executor until a driver implements it.
	return runtimecontract.Describe("helm", "kubernetes-api", runtimecontract.Deploy, runtimecontract.Destroy)
}

func (e SourceAuthExecutor) RuntimeCapabilities(app core.App, server core.Server) runtimecontract.Manifest {
	return executorCapabilities(e.Next, app, server)
}

func (e HookExecutor) RuntimeCapabilities(app core.App, server core.Server) runtimecontract.Manifest {
	return executorCapabilities(e.Next, app, server)
}

func (e SnapshotExecutor) RuntimeCapabilities(app core.App, server core.Server) runtimecontract.Manifest {
	return executorCapabilities(e.Next, app, server)
}
