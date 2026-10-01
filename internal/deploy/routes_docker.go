package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
)

func candidateResourceName(appID, deploymentID string) string {
	sum := sha256.Sum256([]byte(appID + ":" + deploymentID))
	return "dispatch-candidate-" + hex.EncodeToString(sum[:16])
}

func (e DockerExecutor) prepareManagedRoute(ctx context.Context, d core.Deployment, app core.App, server core.Server) (*core.ApplicationRoute, error) {
	plan, err := routing.Plan(d, app, server)
	if err != nil || plan == nil {
		return plan, err
	}
	if e.Routes == nil {
		return nil, errors.New("public routing is not configured on this runtime; set its managed provider directory before deploying")
	}
	prepared, err := e.Routes.Prepare(ctx, *plan)
	if err != nil {
		return nil, err
	}
	if err = ReportRoute(ctx, prepared); err != nil {
		return nil, err
	}
	return &prepared, nil
}
func (e DockerExecutor) candidatePort(ctx context.Context, name string, containerPort int) (int, error) {
	var output strings.Builder
	if err := e.command(ctx, nil, &output, "docker", "port", name, strconv.Itoa(containerPort)+"/tcp"); err != nil {
		return 0, errors.New("cannot inspect the candidate's allocated port")
	}
	lines := strings.Fields(output.String())
	if len(lines) != 1 {
		return 0, errors.New("candidate must have exactly one loopback ingress mapping")
	}
	host, raw, err := net.SplitHostPort(lines[0])
	if err != nil || host != "127.0.0.1" {
		return 0, errors.New("candidate ingress is not private loopback")
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1024 || port > 65535 {
		return 0, errors.New("candidate has an invalid allocated ingress port")
	}
	return port, nil
}

func (e DockerExecutor) promoteCandidate(ctx context.Context, d core.Deployment, app core.App, server core.Server, plan core.ApplicationRoute, targets []CandidateTarget, port int, progress Progress) error {
	app.Domain = plan.Hostname
	if err := e.CheckCandidateHealth(ctx, d, app, server, targets, progress); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	published, err := e.Routes.Promote(ctx, plan, routing.LoopbackDestination(port))
	if err != nil {
		return err
	}
	if err = ReportRoute(ctx, published); err != nil {
		return fmt.Errorf("route was published but controller evidence could not be saved: %w", err)
	}
	return progress(core.DeploymentRouting, "Candidate published at "+plan.Hostname+"; public DNS and certificate verification is pending")
}

func (e DockerExecutor) startRoutedDocker(ctx context.Context, d core.Deployment, app core.App, server core.Server, plan core.ApplicationRoute, image, env string, progress Progress) error {
	name := candidateResourceName(app.ID, d.ID)
	var existing strings.Builder
	if err := e.command(ctx, nil, &existing, "docker", "ps", "-aq", "--filter", "name=^/"+name+"$"); err != nil {
		return errors.New("cannot inspect candidate ownership before deployment")
	}
	if strings.TrimSpace(existing.String()) == "" {
		args := []string{"create", "--name", name, "--label", "dispatch.app=" + app.ID, "--label", "dispatch.project=" + app.ProjectID, "--label", "dispatch.deployment=" + d.ID, "--label", "traefik.enable=false", "-p", "127.0.0.1::" + strconv.Itoa(app.ContainerPort)}
		if env != "" {
			args = append(args, "--env-file", env)
		}
		networks := dockerServiceNetworks(app.ServiceRuntime)
		if len(networks) > 0 {
			args = append(args, "--network", networks[0])
		}
		args = append(args, image)
		if err := e.command(ctx, nil, io.Discard, "docker", args...); err != nil {
			return errors.New("cannot create isolated route candidate; previous destination was retained")
		}
		for index, network := range networks {
			if index == 0 {
				continue
			}
			if err := e.command(ctx, nil, io.Discard, "docker", "network", "connect", network, name); err != nil {
				return errors.New("cannot attach candidate service network")
			}
		}
	} else if err := e.validateContainerOwner(ctx, name, app.ID); err != nil {
		return err
	}
	if err := progress(core.DeploymentStarting, "Starting isolated candidate on "+server.Name); err != nil {
		return err
	}
	if err := e.command(ctx, nil, io.Discard, "docker", "start", name); err != nil {
		return errors.New("cannot start isolated candidate; previous destination was retained")
	}
	port, err := e.candidatePort(ctx, name, app.ContainerPort)
	if err != nil {
		return err
	}
	if err = e.promoteCandidate(ctx, d, app, server, plan, []CandidateTarget{{Container: name, Host: "127.0.0.1", Port: port}}, port, progress); err != nil {
		// Only remove a rejected candidate. A successful file switch may have been
		// followed by a controller/reporting failure and must remain recoverable.
		current, readErr := e.Routes.Read(context.WithoutCancel(ctx), app.ID)
		if readErr == nil && current.DeploymentID != d.ID {
			if e.validateContainerOwner(context.WithoutCancel(ctx), name, app.ID) == nil {
				_ = e.command(context.WithoutCancel(ctx), nil, io.Discard, "docker", "rm", "-f", name)
			}
		}
		return err
	}
	return nil
}
