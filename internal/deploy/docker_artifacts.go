package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

const maxRuntimeArtifactBytes = 32 << 20

var dockerImageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type RuntimeRollbackExecutor interface {
	PreviewRuntimeRollback(context.Context, core.Deployment, core.App, core.Server) (RollbackPreview, error)
	RollbackRuntime(context.Context, core.Deployment, core.Deployment, core.App, core.Server, Progress) error
}

type runtimeFile struct {
	Data []byte `json:"data"`
	Mode uint32 `json:"mode"`
}

type dockerArtifact struct {
	ProjectID     string                       `json:"projectId"`
	Version       int                          `json:"version"`
	BuildType     core.BuildType               `json:"buildType"`
	ContainerPort int                          `json:"containerPort"`
	Domain        string                       `json:"domain"`
	Images        map[string]string            `json:"images"`
	Bindings      []core.ServiceRuntimeBinding `json:"bindings,omitempty"`
	Compose       map[string]any               `json:"compose,omitempty"`
	Files         map[string]runtimeFile       `json:"files,omitempty"`
}

func runtimeArtifactScope(artifact core.RuntimeArtifact) string {
	return "deployment-runtime:" + artifact.ScopeID + ":" + artifact.AppID + ":" + artifact.ServerID
}

func (e DockerExecutor) imageIdentity(ctx context.Context, image string) (string, error) {
	var output strings.Builder
	if err := e.command(ctx, nil, &output, "docker", "image", "inspect", "--format", "{{.Id}}", image); err != nil {
		return "", errors.New("A retained Docker image is unavailable on this target. Deploy the revision again before selecting it for rollback.")
	}
	id := strings.TrimSpace(output.String())
	if !dockerImageID.MatchString(id) {
		return "", errors.New("Docker did not return an immutable image identity.")
	}
	return id, nil
}

func (e DockerExecutor) captureDockerArtifact(ctx context.Context, _ core.Deployment, app core.App, _ core.Server, image string) (dockerArtifact, error) {
	id, err := e.imageIdentity(ctx, image)
	return dockerArtifact{Version: 1, BuildType: app.BuildType, ContainerPort: app.ContainerPort, Domain: app.Domain, Images: map[string]string{"application": id}, Bindings: app.ServiceRuntime}, err
}

func (e DockerExecutor) saveArtifact(ctx context.Context, d core.Deployment, app core.App, server core.Server, inputs dockerArtifact) error {
	inputs.ProjectID = app.ProjectID
	raw, err := json.Marshal(inputs)
	if err != nil || len(raw) > maxRuntimeArtifactBytes {
		return errors.New("Runtime inputs exceed the retained artifact limit or cannot be encoded.")
	}
	defer clear(raw)
	artifact := core.RuntimeArtifact{DeploymentID: d.ID, AppID: app.ID, ServerID: server.ID, ScopeID: d.ID}
	artifact.Ciphertext, err = e.Vault.Encrypt(runtimeArtifactScope(artifact), raw)
	if err != nil {
		return errors.New("Cannot encrypt retained runtime inputs.")
	}
	if err = e.Artifacts.SaveRuntimeArtifact(ctx, artifact); err != nil {
		return errors.New("Cannot save retained runtime inputs. Runtime changes were not started.")
	}
	return nil
}

func (e DockerExecutor) loadArtifact(ctx context.Context, d core.Deployment, app core.App, server core.Server) (dockerArtifact, error) {
	var inputs dockerArtifact
	if e.Artifacts == nil || e.Vault == nil {
		return inputs, errors.New("Encrypted runtime artifact storage is not configured.")
	}
	if !localDockerServer(server) || server.ID != app.ServerID || server.ID != d.Snapshot.TargetID {
		return inputs, errors.New("The retained Docker target changed or is unavailable.")
	}
	artifact, err := e.Artifacts.GetRuntimeArtifact(ctx, d.ID)
	if err != nil || artifact.AppID != app.ID || artifact.ServerID != server.ID {
		return inputs, errors.New("No retained Docker runtime inputs exist for this deployment. Deploy once with artifact retention enabled.")
	}
	raw, err := e.Vault.Decrypt(runtimeArtifactScope(artifact), artifact.Ciphertext)
	if err != nil {
		return inputs, errors.New("The retained runtime inputs could not be decrypted.")
	}
	defer clear(raw)
	if len(raw) > maxRuntimeArtifactBytes || json.Unmarshal(raw, &inputs) != nil || inputs.Version != 1 || len(inputs.Images) == 0 {
		return inputs, errors.New("The retained runtime inputs are invalid or use an unsupported version.")
	}
	if inputs.ProjectID != app.ProjectID {
		return inputs, errors.New("The application's project changed. Retained runtime inputs cannot cross project boundaries.")
	}
	if inputs.BuildType == core.BuildTypeDockerfile && (len(inputs.Images) != 1 || !dockerImageID.MatchString(inputs.Images["application"])) {
		return inputs, errors.New("The retained container image is invalid.")
	}
	if inputs.BuildType == core.BuildTypeCompose {
		if err = validateComposeArtifact(inputs, app.ID); err != nil {
			return inputs, err
		}
	}
	if inputs.BuildType != app.BuildType || inputs.BuildType != core.BuildTypeDockerfile && inputs.BuildType != core.BuildTypeCompose {
		return inputs, errors.New("The application's runtime type changed. Historical rollback cannot change runtimes.")
	}
	for _, id := range inputs.Images {
		if !dockerImageID.MatchString(id) {
			return inputs, errors.New("The retained inputs do not identify immutable images.")
		}
		actual, err := e.imageIdentity(ctx, id)
		if err != nil || actual != id {
			return inputs, errors.New("A retained Docker image is missing. Restore the image or deploy this revision again before rollback.")
		}
	}
	if err = e.validateDockerOwnership(ctx, app); err != nil {
		return inputs, err
	}
	if inputs.BuildType == core.BuildTypeCompose {
		if err = validateComposeRuntimeFiles(inputs.Compose); err != nil {
			return inputs, err
		}
	}
	return inputs, nil
}

func localDockerServer(server core.Server) bool {
	return server.AgentNodeID == "" && (server.Address == "local" || server.Address == "127.0.0.1" || server.Address == "localhost")
}

func (e DockerExecutor) PreviewRuntimeRollback(ctx context.Context, d core.Deployment, app core.App, server core.Server) (RollbackPreview, error) {
	inputs, err := e.loadArtifact(ctx, d, app, server)
	if err != nil {
		return RollbackPreview{}, err
	}
	resources := []ReleaseResource{}
	for _, service := range sortedImageServices(inputs.Images) {
		kind, name := "Container", dockerResourceName(app.ID)
		if inputs.BuildType == core.BuildTypeCompose {
			kind, name = "ComposeService", service
		}
		resources = append(resources, ReleaseResource{Kind: kind, Name: name})
	}
	version, err := e.dockerRuntimeDigest(ctx, app)
	if err != nil {
		return RollbackPreview{}, err
	}
	port := inputs.ContainerPort
	ports := []string{}
	if inputs.BuildType == core.BuildTypeCompose {
		port = 0
		ports = composePublishedPorts(inputs.Compose)
	}
	return RollbackPreview{Ports: ports, Available: true, Runtime: string(inputs.BuildType), Target: server.Name, Images: inputs.Images, Domain: inputs.Domain, ContainerPort: port, Resources: resources, RuntimeDigest: version}, nil
}

func sortedImageServices(images map[string]string) []string {
	names := make([]string, 0, len(images))
	for name := range images {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (e DockerExecutor) artifactDirectory(appID, deploymentID string) string {
	root := e.ArtifactDirectory
	if root == "" {
		root = filepath.Join(os.TempDir(), "dispatch-runtime-artifacts")
	}
	return filepath.Join(root, dockerResourceName(appID), safeID.ReplaceAllString(deploymentID, "-"))
}

func (e DockerExecutor) validateDockerOwnership(ctx context.Context, app core.App) error {
	var output strings.Builder
	filter := "name=^/" + dockerResourceName(app.ID) + "$"
	if app.BuildType == core.BuildTypeCompose {
		filter = "label=com.docker.compose.project=" + dockerResourceName(app.ID)
	}
	if err := e.command(ctx, nil, &output, "docker", "ps", "-a", "--filter", filter, "--format", "{{.ID}}"); err != nil {
		return errors.New("Cannot verify Docker resource ownership.")
	}
	for _, id := range strings.Fields(output.String()) {
		if err := e.validateContainerOwner(ctx, id, app.ID); err != nil {
			return err
		}
	}
	return nil
}

func (e DockerExecutor) dockerRuntimeDigest(ctx context.Context, app core.App) (string, error) {
	var output strings.Builder
	filter := "name=^/" + dockerResourceName(app.ID) + "$"
	if app.BuildType == core.BuildTypeCompose {
		filter = "label=com.docker.compose.project=" + dockerResourceName(app.ID)
	}
	if err := e.command(ctx, nil, &output, "docker", "ps", "-a", "--filter", filter, "--format", "{{.ID}}"); err != nil {
		return "", errors.New("Cannot inspect the current runtime identity.")
	}
	ids := strings.Fields(output.String())
	sort.Strings(ids)
	var state strings.Builder
	for _, id := range ids {
		if err := e.command(ctx, nil, &state, "docker", "inspect", "--format", `{{.Id}} {{.Image}} {{json .Config.Labels}} {{.State.Running}}`, id); err != nil {
			return "", errors.New("The current runtime changed during review. Check it again.")
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(state.String()))), nil
}

func (e DockerExecutor) RollbackRuntime(ctx context.Context, d, source core.Deployment, app core.App, server core.Server, progress Progress) error {
	inputs, err := e.loadArtifact(ctx, source, app, server)
	if err != nil {
		return err
	}
	if inputs.BuildType == core.BuildTypeCompose {
		return e.applyComposeArtifact(ctx, d, app, server, inputs, progress)
	}
	workspace, err := os.MkdirTemp("", "dispatch-docker-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)
	app.ServiceRuntime, app.ContainerPort, app.Domain = inputs.Bindings, inputs.ContainerPort, inputs.Domain
	env, err := dockerServiceEnv(workspace, inputs.Bindings)
	if err != nil {
		return errors.New("Cannot materialize the retained environment.")
	}
	if err = progress(core.DeploymentStarting, "Restoring retained Docker image on "+server.Name); err != nil {
		return err
	}
	name := dockerResourceName(app.ID)
	if err = e.removeOwnedContainer(ctx, app); err != nil {
		return err
	}

	networks := dockerServiceNetworks(inputs.Bindings)
	args := []string{"create", "--name", name, "--label", "dispatch.app=" + app.ID, "--label", "dispatch.deployment=" + d.ID}
	if inputs.ContainerPort > 0 {
		args = append(args, "-p", fmt.Sprintf("%d:%d", inputs.ContainerPort, inputs.ContainerPort))
	}
	if env != "" {
		args = append(args, "--env-file", env)
	}
	if len(networks) > 0 {
		args = append(args, "--network", networks[0])
	}
	args = append(args, inputs.Images["application"])
	if err = e.command(ctx, nil, io.Discard, "docker", args...); err != nil {
		return errors.New("Cannot create the retained container. Inspect runtime state before retrying.")
	}
	if len(networks) > 1 {
		for _, network := range networks[1:] {
			if err = e.command(ctx, nil, io.Discard, "docker", "network", "connect", network, name); err != nil {
				return errors.New("Cannot attach a retained service network.")
			}
		}
	}
	if err = e.command(ctx, nil, io.Discard, "docker", "start", name); err != nil {
		return errors.New("Cannot start the retained container.")
	}
	if err = progress(core.DeploymentChecking, "Checking retained container readiness"); err != nil {
		return err
	}
	if err = e.waitContainer(ctx, name); err != nil {
		return err
	}
	return progress(core.DeploymentRouting, routeMessage(app))
}

func (e DockerExecutor) waitContainer(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	for {
		var output strings.Builder
		if err := e.command(ctx, nil, &output, "docker", "inspect", "--format", "{{json .State}}", name); err != nil {
			return errors.New("Cannot inspect restored container readiness.")
		}
		var state struct {
			Running bool
			Health  *struct{ Status string }
		}
		if json.Unmarshal([]byte(output.String()), &state) != nil {
			return errors.New("Docker returned invalid container readiness.")
		}
		if state.Running && (state.Health == nil || state.Health.Status == "healthy") {
			return nil
		}
		if !state.Running || state.Health != nil && state.Health.Status == "unhealthy" {
			return errors.New("The restored container failed readiness. Inspect its logs before retrying.")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (e DockerExecutor) cleanupOwnedDocker(ctx context.Context, app core.App, server core.Server, progress Progress) error {
	if err := e.validateDockerOwnership(ctx, app); err != nil {
		return err
	}
	if err := progress(core.DeploymentStarting, "Removing owned Docker resources on "+server.Name); err != nil {
		return err
	}
	filter := "name=^/" + dockerResourceName(app.ID) + "$"
	if app.BuildType == core.BuildTypeCompose {
		filter = "label=com.docker.compose.project=" + dockerResourceName(app.ID)
	}
	var containers strings.Builder
	if err := e.command(ctx, nil, &containers, "docker", "ps", "-a", "--filter", filter, "--format", "{{.ID}}"); err != nil {
		return errors.New("Cannot list owned containers for cleanup.")
	}
	for _, id := range strings.Fields(containers.String()) {
		if err := e.validateContainerOwner(ctx, id, app.ID); err != nil {
			return err
		}
		if err := e.command(ctx, nil, io.Discard, "docker", "rm", "-f", id); err != nil {
			return errors.New("Container cleanup failed. Review the remaining owned resources before retrying.")
		}
	}
	if app.BuildType == core.BuildTypeCompose {
		var networks strings.Builder
		if err := e.command(ctx, nil, &networks, "docker", "network", "ls", "--filter", "label=com.docker.compose.project="+dockerResourceName(app.ID), "--filter", "label=dispatch.app="+app.ID, "--format", "{{.ID}}"); err != nil {
			return errors.New("Cannot list owned Compose networks.")
		}
		for _, id := range strings.Fields(networks.String()) {
			if err := e.command(ctx, nil, io.Discard, "docker", "network", "rm", id); err != nil {
				return errors.New("An owned Compose network is still in use. Check connected containers before retrying cleanup.")
			}
		}
	}
	if err := os.RemoveAll(filepath.Dir(e.artifactDirectory(app.ID, "unused"))); err != nil {
		return errors.New("Runtime cleanup succeeded but retained runtime files could not be removed.")
	}
	return progress(core.DeploymentSucceeded, "Owned Docker resources removed; volume and external database data preserved")
}

func (e DockerExecutor) validateContainerOwner(ctx context.Context, id, appID string) error {
	var owner strings.Builder
	if err := e.command(ctx, nil, &owner, "docker", "inspect", "--format", `{{index .Config.Labels "dispatch.app"}}`, id); err != nil || strings.TrimSpace(owner.String()) != appID {
		return errors.New("A Docker resource at this name is not owned by this application. Resolve the ownership conflict before continuing.")
	}
	return nil
}

func (e DockerExecutor) removeOwnedContainer(ctx context.Context, app core.App) error {
	var containers strings.Builder
	if err := e.command(ctx, nil, &containers, "docker", "ps", "-a", "--filter", "name=^/"+dockerResourceName(app.ID)+"$", "--format", "{{.ID}}"); err != nil {
		return errors.New("Cannot list the current container.")
	}
	for _, id := range strings.Fields(containers.String()) {
		if err := e.validateContainerOwner(ctx, id, app.ID); err != nil {
			return err
		}
		if err := e.command(ctx, nil, io.Discard, "docker", "rm", "-f", id); err != nil {
			return errors.New("Cannot stop the current owned container. Review runtime state before retrying.")
		}
	}
	return nil
}

func (e DockerExecutor) CurrentRuntimeIdentity(ctx context.Context, app core.App, server core.Server) (string, error) {
	if !localDockerServer(server) || server.ID != app.ServerID {
		return "", errors.New("The Docker target is unavailable.")
	}
	if err := e.validateDockerOwnership(ctx, app); err != nil {
		return "", err
	}
	return e.dockerRuntimeDigest(ctx, app)
}
