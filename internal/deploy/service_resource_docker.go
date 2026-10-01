package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

func serviceInspection(req core.ServiceProvisionRequest, server core.Server, provider string) core.ServiceResourceInspection {
	return core.ServiceResourceInspection{RunID: req.Run.ID, ProjectID: req.Run.ProjectID, ServerID: server.ID, Provider: provider, State: "absent", StorageRetained: true}
}

func (e DockerExecutor) InspectServiceResource(ctx context.Context, req core.ServiceProvisionRequest, server core.Server) (core.ServiceResourceInspection, error) {
	result := serviceInspection(req, server, "docker")
	if server.AgentNodeID != "" {
		return result, errors.New("agent-bound services require remote inspection")
	}
	if err := ValidateServiceTarget(server, "docker"); err != nil {
		return result, err
	}
	name := ServiceResourceName(req.Run.ID)
	var ids strings.Builder
	if e.command(ctx, nil, &ids, "docker", "ps", "-aq", "--no-trunc", "--filter", "name=^/"+name+"$") != nil {
		return result, errors.New("cannot inspect service ownership")
	}
	if strings.TrimSpace(ids.String()) == "" {
		return result, nil
	}
	if len(strings.Fields(ids.String())) != 1 {
		return result, errors.New("service identity is ambiguous")
	}
	var raw strings.Builder
	const format = `{"id":{{json .Id}},"labels":{{json .Config.Labels}},"state":{{json .State}}}`
	if e.command(ctx, nil, &raw, "docker", "inspect", "--format", format, strings.TrimSpace(ids.String())) != nil {
		return result, errors.New("cannot inspect service ownership")
	}
	var saved struct {
		ID     string            `json:"id"`
		Labels map[string]string `json:"labels"`
		State  struct {
			Status string
			Health *struct{ Status string }
		} `json:"state"`
	}
	if json.Unmarshal([]byte(raw.String()), &saved) != nil || saved.ID != strings.TrimSpace(ids.String()) {
		return result, errors.New("service inspection returned invalid identity")
	}
	for key, value := range provisionLabels(req) {
		if saved.Labels[key] != value {
			return result, errors.New("service resource ownership does not match its project, template and provision run")
		}
	}
	result.ResourceID, result.State = saved.ID, "unready"
	if saved.State.Status == "running" && saved.State.Health != nil && saved.State.Health.Status == "healthy" {
		result.State = "ready"
	}
	return result, nil
}

func (e DockerExecutor) DeleteServiceResource(ctx context.Context, req core.ServiceProvisionRequest, server core.Server, expected string) error {
	current, err := e.InspectServiceResource(ctx, req, server)
	if err != nil {
		return err
	}
	if current.State == "absent" {
		return nil
	}
	if expected == "" || current.ResourceID != expected {
		return errors.New("service resource changed after deletion review")
	}
	// Never remove volumes. The immutable container ID prevents name reuse from
	// deleting a replacement resource after the ownership check.
	if e.command(ctx, nil, io.Discard, "docker", "rm", "-f", current.ResourceID) != nil {
		return errors.New("service cleanup did not complete; inspect the same resource before retrying")
	}
	return nil
}

func (e DockerExecutor) checkServiceVolume(ctx context.Context, req core.ServiceProvisionRequest) error {
	name := ServiceResourceName(req.Run.ID) + "-data"
	var names strings.Builder
	if e.command(ctx, nil, &names, "docker", "volume", "ls", "--filter", "name=^"+name+"$", "--format", "{{.Name}}") != nil {
		return errors.New("cannot inspect retained service storage")
	}
	if strings.TrimSpace(names.String()) == "" {
		return nil
	}
	if req.Password == "" {
		return errors.New("retained service storage requires the original encrypted credentials")
	}
	var raw strings.Builder
	if e.command(ctx, nil, &raw, "docker", "volume", "inspect", "--format", "{{json .Labels}}", name) != nil {
		return errors.New("cannot inspect retained service storage ownership")
	}
	var labels map[string]string
	if json.Unmarshal([]byte(raw.String()), &labels) != nil {
		return errors.New("invalid retained service storage ownership")
	}
	for key, value := range provisionLabels(req) {
		if labels[key] != value {
			return errors.New("retained service storage belongs to a different provision run")
		}
	}
	return nil
}
