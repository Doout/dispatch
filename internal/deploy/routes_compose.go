package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

// Staging arbitrary stateful Compose projects is unsafe. Managed routes accept
// stateless application services; durable data belongs in a separately bound
// service or a read-only mount. Rejections happen before any Compose apply.
func routedComposeConfig(config map[string]any, app core.App, server core.Server, d core.Deployment) (map[string]any, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	var candidate map[string]any
	if json.Unmarshal(raw, &candidate) != nil {
		return nil, errors.New("invalid managed Compose inputs")
	}
	services, ok := candidate["services"].(map[string]any)
	if !ok {
		return nil, errors.New("Compose has no services")
	}
	ingress, _ := candidate["x-dispatch-ingress-service"].(string)
	if ingress == "" {
		ingress = server.Routing.ComposeService
	}
	if ingress == "" && len(services) == 1 {
		for name := range services {
			ingress = name
		}
	}
	candidate["x-dispatch-ingress-service"] = ingress
	if _, ok := services[ingress]; !ok {
		return nil, errors.New("configured Compose ingress service does not exist")
	}
	for name, item := range services {
		service, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("invalid Compose service")
		}
		if service["container_name"] != nil || service["network_mode"] != nil || service["external_links"] != nil {
			return nil, errors.New("managed Compose routes require isolated service names and networks")
		}
		if deployment, ok := service["deploy"].(map[string]any); ok {
			if replicas, ok := deployment["replicas"].(float64); ok && replicas != 1 {
				return nil, errors.New("managed Compose routes currently require one replica per service")
			}
		}
		if mounts, ok := service["volumes"].([]any); ok {
			for _, mount := range mounts {
				entry, ok := mount.(map[string]any)
				if !ok || entry["read_only"] != true {
					return nil, errors.New("managed Compose candidates require read-only mounts; bind a separate durable service for writable data")
				}
			}
		}
		if ports, ok := service["ports"].([]any); ok && len(ports) > 0 {
			if name != ingress {
				return nil, errors.New("only the configured Compose ingress service may publish a port")
			}
			for _, port := range ports {
				entry, ok := port.(map[string]any)
				if !ok || entry["target"] != float64(app.ContainerPort) || entry["protocol"] != nil && entry["protocol"] != "tcp" {
					return nil, errors.New("managed Compose supports one configured TCP ingress port")
				}
			}
		}
		delete(service, "ports")
		labels, _ := service["labels"].(map[string]any)
		if labels == nil {
			labels = map[string]any{}
		}
		labels["dispatch.app"], labels["dispatch.project"], labels["dispatch.deployment"], labels["traefik.enable"] = app.ID, app.ProjectID, d.ID, "false"
		labels["dispatch.route-candidate"] = "true"
		service["labels"] = labels
		if name == ingress {
			service["ports"] = []map[string]any{{"target": app.ContainerPort, "host_ip": "127.0.0.1", "published": "0", "protocol": "tcp"}}
		}
	}
	project := candidateResourceName(app.ID, d.ID)
	candidate["name"] = project
	if networks, ok := candidate["networks"].(map[string]any); ok {
		for key, item := range networks {
			network, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("invalid Compose network")
			}
			if network["external"] == true {
				continue
			}
			network["name"] = project + "_" + key
			network["labels"] = map[string]string{"dispatch.app": app.ID, "dispatch.project": app.ProjectID, "dispatch.deployment": d.ID, "dispatch.route-candidate": "true"}
		}
	}
	return candidate, nil
}

func (e DockerExecutor) applyRoutedCompose(ctx context.Context, d core.Deployment, app core.App, server core.Server, inputs dockerArtifact, progress Progress) error {
	candidate, err := routedComposeConfig(inputs.Compose, app, server, d)
	if err != nil {
		return err
	}
	inputs.Compose = candidate
	plan, err := e.prepareManagedRoute(ctx, d, app, server)
	if err != nil {
		return err
	}
	if plan == nil {
		return errors.New("managed Compose route is unavailable")
	}
	path, err := e.materializeCompose(d, app, inputs)
	if err != nil {
		return errors.New("cannot prepare isolated Compose runtime files")
	}
	name := candidateResourceName(app.ID, d.ID)
	ingress, _ := candidate["x-dispatch-ingress-service"].(string)
	// Compose must not reconcile a deterministic project name that an external
	// actor replaced with a different accepted revision or another owner's data.
	var existing strings.Builder
	if err = e.command(ctx, nil, &existing, "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+name); err != nil {
		return err
	}
	for _, id := range strings.Fields(existing.String()) {
		if _, err = e.routeComposeIdentity(ctx, id, app, d.ID, inputs.Images); err != nil {
			return err
		}
	}
	if err = progress(core.DeploymentStarting, "Starting isolated Compose candidate on "+server.Name); err != nil {
		return err
	}
	if err = e.command(ctx, nil, io.Discard, "docker", "compose", "-p", name, "-f", path, "up", "-d", "--no-build", "--pull", "never", "--remove-orphans"); err != nil {
		return errors.New("isolated Compose apply failed; previous healthy route was retained")
	}
	var output strings.Builder
	if err = e.command(ctx, nil, &output, "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+name); err != nil {
		return errors.New("cannot inspect isolated Compose candidates")
	}
	targets := []CandidateTarget{}
	ingressPort := 0
	for _, id := range strings.Fields(output.String()) {
		service, identityErr := e.routeComposeIdentity(ctx, id, app, d.ID, inputs.Images)
		if identityErr != nil {
			return identityErr
		}
		target := CandidateTarget{Container: id, Service: service}
		if service == ingress {
			if ingressPort != 0 {
				return errors.New("Compose ingress resolves to more than one candidate")
			}
			ingressPort, err = e.candidatePort(ctx, id, app.ContainerPort)
			if err != nil {
				return err
			}
			target.Host, target.Port = "127.0.0.1", ingressPort
		}
		targets = append(targets, target)
	}
	if len(targets) == 0 || ingressPort == 0 {
		return errors.New("Compose candidate has no configured ingress endpoint")
	}
	if err = e.promoteCandidate(ctx, d, app, server, *plan, targets, ingressPort, progress); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		current, readErr := e.Routes.Read(cleanup, app.ID)
		if readErr == nil && current.DeploymentID != d.ID {
			_ = e.command(cleanup, nil, io.Discard, "docker", "compose", "-p", name, "-f", path, "down")
		}
		return err
	}
	return nil
}

func (e DockerExecutor) routeComposeIdentity(ctx context.Context, id string, app core.App, deploymentID string, images map[string]string) (string, error) {
	var metadata strings.Builder
	if err := e.command(ctx, nil, &metadata, "docker", "inspect", "--format", `{{index .Config.Labels "com.docker.compose.service"}}`, id); err != nil {
		return "", errors.New("cannot inspect Compose service identity")
	}
	service := strings.TrimSpace(metadata.String())
	return service, e.validateRouteCandidate(ctx, id, app, deploymentID, images[service])
}
