package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func postgresProvisionURL(fields map[string]string) string {
	u := &url.URL{Scheme: "postgresql", Host: net.JoinHostPort(fields["host"], fields["port"]), Path: "/" + fields["database"], User: url.UserPassword(fields["username"], fields["password"])}
	q := url.Values{"sslmode": {fields["sslmode"]}}
	u.RawQuery = q.Encode()
	return u.String()
}

// Provision uses Docker's ordinary image, volume, network and health APIs.
// No provider script or source repository is involved.
func (e DockerExecutor) Provision(ctx context.Context, req core.ServiceProvisionRequest, spec core.DockerServiceProvision, server core.Server) (map[string]string, error) {
	if server.AgentNodeID != "" {
		return nil, errors.New("agent-bound services must execute through the remote runtime")
	}
	if err := ValidateServiceTarget(server, "docker"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, helmOperationTimeout)
	defer cancel()
	variables, err := provisionVariables(req, "")
	if err != nil {
		return nil, err
	}
	name := variables["service.resource"]
	image, network, mount := spec.Image, spec.Network, spec.StorageMountPath
	if image == "" {
		image = DefaultServiceImage
	}
	if network == "" {
		network = DefaultServiceNetwork
	}
	environment := map[string]string{}
	health := append([]string(nil), spec.Healthcheck...)
	if req.ServiceType == "postgresql" {
		environment = map[string]string{"POSTGRES_DB": variables["service.database"], "POSTGRES_USER": variables["service.username"], "POSTGRES_PASSWORD": variables["service.password"], "PGDATA": "/var/lib/postgresql/data/pgdata"}
		if mount == "" {
			mount = "/var/lib/postgresql/data"
		}
		if len(health) == 0 {
			health = []string{"pg_isready", "-h", "127.0.0.1", "-U", variables["service.username"], "-d", variables["service.database"]}
		}
	}
	for key, value := range spec.Environment {
		environment[key], err = provisionString(value, variables)
		if err != nil {
			return nil, err
		}
	}
	for i, value := range health {
		health[i], err = provisionString(value, variables)
		if err != nil {
			return nil, err
		}
	}
	outputs, err := provisionOutputs(req, spec.Connection, variables, name)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "dispatch-service-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	envPath := filepath.Join(root, "environment")
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var content strings.Builder
	for _, key := range keys {
		if strings.ContainsAny(environment[key], "\r\n\x00") {
			return nil, errors.New("Docker environment values must be single-line")
		}
		content.WriteString(key + "=" + environment[key] + "\n")
	}
	if err = os.WriteFile(envPath, []byte(content.String()), 0600); err != nil {
		return nil, err
	}
	call := func(args ...string) error { return e.command(ctx, nil, io.Discard, "docker", args...) }
	if call("network", "inspect", network) != nil {
		if call("network", "create", network) != nil && call("network", "inspect", network) != nil {
			return nil, errors.New("cannot create the service network; check Docker access")
		}
	}
	labels := provisionLabels(req)
	labelArgs := []string{}
	for _, key := range []string{"dispatch.managed-by", "dispatch.project", "dispatch.service-template", "dispatch.service-provision"} {
		labelArgs = append(labelArgs, "--label", key+"="+labels[key])
	}
	args := append([]string{"run", "-d", "--name", name, "--network", network, "--restart", "unless-stopped", "--memory", "512m", "--cpus", "0.5", "--env-file", envPath}, labelArgs...)
	if mount != "" {
		volume := name + "-data"
		volumeArgs := append([]string{"volume", "create"}, labelArgs...)
		volumeArgs = append(volumeArgs, volume)
		if call(volumeArgs...) != nil {
			return nil, errors.New("cannot create persistent service storage")
		}
		args = append(args, "--mount", "type=volume,source="+volume+",target="+mount)
	}
	if len(health) > 0 {
		quoted := []string{}
		for _, arg := range health {
			quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", "'\"'\"'")+"'")
		}
		args = append(args, "--health-cmd", strings.Join(quoted, " "), "--health-interval", "2s", "--health-timeout", "5s", "--health-start-period", "10s", "--health-retries", "30")
	}
	args = append(args, image)
	if call(args...) != nil {
		return nil, errors.New("cannot start the service container; check image access and Docker resources")
	}
	// A failed provision removes only its own candidate container. Keep the data
	// volume, with provenance labels, so failures never erase database contents.
	ready := false
	defer func() {
		if !ready {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_ = e.command(cleanup, nil, io.Discard, "docker", "rm", "-f", name)
		}
	}()
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		var state struct {
			Status string
			Health *struct{ Status string }
		}
		var output strings.Builder
		if e.command(ctx, nil, &output, "docker", "inspect", "--format", "{{json .State}}", name) != nil {
			return nil, errors.New("cannot inspect the service container")
		}
		if json.Unmarshal([]byte(output.String()), &state) != nil {
			return nil, errors.New("Docker returned an invalid container status")
		}
		if state.Status == "exited" || state.Status == "dead" || state.Health != nil && state.Health.Status == "unhealthy" {
			return nil, errors.New("service container failed its readiness check; check the image and settings on the Docker server. Its data volume was retained")
		}
		if state.Status == "running" && state.Health != nil && state.Health.Status == "healthy" {
			ready = true
			return outputs, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("service readiness timed out; inspect the Docker server before retrying")
		case <-timer.C:
		}
	}
}
