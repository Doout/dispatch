package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

func (e DockerExecutor) deployRetainedCompose(ctx context.Context, d core.Deployment, app core.App, server core.Server, workspace string, args []string, progress Progress) error {
	config, err := e.composeConfig(ctx, workspace, args)
	if err != nil {
		return err
	}
	filterComposeProfiles(config)
	services, ok := config["services"].(map[string]any)
	if !ok || len(services) == 0 {
		return errors.New("Compose has no active services to deploy.")
	}
	// Build outputs get a per-deployment tag before resolving their image IDs.
	buildImages := map[string]any{}
	for name, item := range services {
		service, ok := item.(map[string]any)
		if !ok {
			return errors.New("Invalid Compose service definition.")
		}
		if _, built := service["build"]; built {
			buildImages[name] = map[string]any{"image": "dispatch/compose-" + strings.ToLower(app.ID) + ":" + strings.ToLower(d.ID) + "-" + safeID.ReplaceAllString(name, "-")}
		}
	}
	if len(buildImages) > 0 {
		path := filepath.Join(workspace, "dispatch-images.json")
		raw, _ := json.Marshal(map[string]any{"services": buildImages})
		if err = os.WriteFile(path, raw, 0600); err != nil {
			return err
		}
		args = append(args, "-f", path)
		if err = e.command(ctx, nil, io.Discard, "docker", append(append([]string{}, args...), "build", "--pull")...); err != nil {
			return errors.New("Compose image build failed; no runtime changes were started.")
		}
	}
	if err = e.command(ctx, nil, io.Discard, "docker", append(append([]string{}, args...), "pull", "--ignore-buildable")...); err != nil {
		return errors.New("Compose image pull failed; no runtime changes were started.")
	}
	config, err = e.composeConfig(ctx, workspace, args)
	if err != nil {
		return err
	}
	inputs := dockerArtifact{Version: 1, BuildType: core.BuildTypeCompose, Domain: app.Domain, ContainerPort: app.ContainerPort, Images: map[string]string{}, Compose: config, Files: map[string]runtimeFile{}}
	filterComposeProfiles(config)
	services = config["services"].(map[string]any)
	for _, name := range sortedComposeServices(services) {
		service := services[name].(map[string]any)
		image, _ := service["image"].(string)
		if image == "" {
			return errors.New("A Compose service has no resolved image.")
		}
		id, err := e.imageIdentity(ctx, image)
		if err != nil {
			return err
		}
		inputs.Images[name] = id
		service["image"], service["pull_policy"] = id, "never"
		delete(service, "build")
		delete(service, "develop")
		labels, _ := service["labels"].(map[string]any)
		if labels == nil {
			labels = map[string]any{}
		}
		labels["dispatch.app"], labels["dispatch.deployment"] = app.ID, d.ID
		service["labels"] = labels
	}
	if err = retainComposeRuntimeFiles(&inputs, workspace); err != nil {
		return err
	}
	if networks, ok := inputs.Compose["networks"].(map[string]any); ok {
		for _, item := range networks {
			network, ok := item.(map[string]any)
			if !ok || network["external"] == true {
				continue
			}
			labels, _ := network["labels"].(map[string]any)
			if labels == nil {
				labels = map[string]any{}
			}
			labels["dispatch.app"] = app.ID
			network["labels"] = labels
		}
	}
	if err = e.saveArtifact(ctx, d, app, server, inputs); err != nil {
		return err
	}
	if err = e.validateDockerOwnership(ctx, app); err != nil {
		return err
	}
	return e.applyComposeArtifact(ctx, d, app, server, inputs, progress)
}

func sortedComposeServices(services map[string]any) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (e DockerExecutor) composeConfig(ctx context.Context, workspace string, args []string) (map[string]any, error) {
	path := filepath.Join(workspace, "dispatch-config.json")
	if err := e.command(ctx, nil, io.Discard, "docker", append(append([]string{}, args...), "config", "--format", "json", "--output", path)...); err != nil {
		return nil, errors.New("Cannot resolve Compose runtime inputs.")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > maxRuntimeArtifactBytes {
		return nil, errors.New("Compose runtime inputs are unavailable or too large.")
	}
	var config map[string]any
	if json.Unmarshal(raw, &config) != nil {
		return nil, errors.New("Compose returned invalid runtime inputs.")
	}
	return config, nil
}

// Only files referenced by the Compose runtime are retained. Shared host mounts
// remain references; database volume contents are never copied or rolled back.
func composeFilePaths(config map[string]any) []map[string]any {
	refs := []map[string]any{}
	services, _ := config["services"].(map[string]any)
	for _, item := range services {
		service, _ := item.(map[string]any)
		volumes, _ := service["volumes"].([]any)
		for _, item := range volumes {
			volume, _ := item.(map[string]any)
			if volume["type"] == "bind" {
				refs = append(refs, volume)
			}
		}
	}
	for _, field := range []string{"configs", "secrets"} {
		entries, _ := config[field].(map[string]any)
		for _, item := range entries {
			entry, _ := item.(map[string]any)
			if entry["file"] != nil {
				refs = append(refs, entry)
			}
		}
	}
	return refs
}

const retainedFilePrefix = "dispatch-retained://"

func retainComposeRuntimeFiles(inputs *dockerArtifact, workspace string) error {
	total := 0
	for _, ref := range composeFilePaths(inputs.Compose) {
		key := "source"
		if ref["file"] != nil {
			key = "file"
		}
		path, _ := ref[key].(string)
		relative, err := filepath.Rel(workspace, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			continue
		}
		if relative == "." {
			return errors.New("Mounting the complete source checkout is not supported for retained Compose releases. Select the required runtime files.")
		}
		if key == "source" && ref["read_only"] != true {
			return errors.New("Source checkout mounts must be read-only for retained releases. Use a persistent host path or named volume for writable data.")
		}
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil || resolved != path {
			return errors.New("Runtime source mounts must not traverse symbolic links.")
		}
		err = filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("Runtime source mounts must not contain symbolic links.")
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(workspace, path)
			if len(inputs.Files) >= 4096 {
				return errors.New("Too many retained runtime files.")
			}
			if entry.IsDir() {
				inputs.Files[filepath.ToSlash(rel)] = runtimeFile{Mode: uint32(os.ModeDir | info.Mode().Perm())}
				return nil
			}
			if err != nil || !info.Mode().IsRegular() {
				return errors.New("Runtime source mounts must contain regular files.")
			}
			if info.Size() > int64(maxRuntimeArtifactBytes-total) || len(inputs.Files) >= 4096 {
				return errors.New("Runtime source files exceed the retained artifact limit.")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			total += len(raw)
			inputs.Files[filepath.ToSlash(rel)] = runtimeFile{Data: raw, Mode: uint32(info.Mode().Perm())}
			return nil
		})
		if err != nil {
			return errors.New("Cannot retain a Compose runtime source mount. Check its files and artifact size.")
		}
		ref[key] = retainedFilePrefix + filepath.ToSlash(relative)
	}
	return validateComposeRuntimeFiles(inputs.Compose)
}

func validateComposeRuntimeFiles(config map[string]any) error {
	for _, ref := range composeFilePaths(config) {
		key := "source"
		if ref["file"] != nil {
			key = "file"
		}
		path, _ := ref[key].(string)
		if strings.HasPrefix(path, retainedFilePrefix) {
			continue
		}
		if path == "" {
			return errors.New("A Compose runtime file reference is missing.")
		}
		if _, err := os.Stat(path); err != nil {
			return errors.New("A shared host mount or external Compose file is unavailable. Restore it before rollback.")
		}
	}
	return nil
}

func (e DockerExecutor) materializeCompose(d core.Deployment, app core.App, inputs dockerArtifact) (string, error) {
	if err := validateComposeArtifact(inputs, app.ID); err != nil {
		return "", err
	}
	dir := e.artifactDirectory(app.ID, d.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	for name, file := range inputs.Files {
		path, err := within(filepath.Join(dir, "files"), filepath.FromSlash(name))
		if err != nil || name == "" || filepath.IsAbs(name) {
			return "", errors.New("Invalid retained runtime file path.")
		}
		if os.FileMode(file.Mode).IsDir() {
			if err = os.MkdirAll(path, 0700); err != nil {
				return "", err
			}
			continue
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", err
		}
		if err = os.WriteFile(path, file.Data, os.FileMode(file.Mode&0777)); err != nil {
			return "", err
		}
	}
	// The protected artifact root stays private. Mount directories preserve the
	// source permissions so processes running as a non-root UID can read them.
	for name, file := range inputs.Files {
		if !os.FileMode(file.Mode).IsDir() {
			continue
		}
		path, err := within(filepath.Join(dir, "files"), filepath.FromSlash(name))
		if err != nil {
			return "", err
		}
		if err = os.Chmod(path, os.FileMode(file.Mode&0777)); err != nil {
			return "", err
		}
	}
	// Round-trip before rewriting so a second restore cannot mutate saved inputs.
	raw, err := json.Marshal(inputs.Compose)
	if err != nil {
		return "", err
	}
	var config map[string]any
	if err = json.Unmarshal(raw, &config); err != nil {
		return "", err
	}
	for _, ref := range composeFilePaths(config) {
		key := "source"
		if ref["file"] != nil {
			key = "file"
		}
		path, _ := ref[key].(string)
		if relative, ok := strings.CutPrefix(path, retainedFilePrefix); ok {
			path, err = within(filepath.Join(dir, "files"), filepath.FromSlash(relative))
			if err != nil {
				return "", err
			}
			ref[key] = path
		}
	}
	services, _ := config["services"].(map[string]any)
	for _, item := range services {
		service := item.(map[string]any)
		labels := service["labels"].(map[string]any)
		labels["dispatch.deployment"] = d.ID
	}
	raw, err = json.Marshal(config)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "dispatch-compose.json")
	return path, os.WriteFile(path, raw, 0600)
}

func (e DockerExecutor) applyComposeArtifact(ctx context.Context, d core.Deployment, app core.App, server core.Server, inputs dockerArtifact, progress Progress) error {
	path, err := e.materializeCompose(d, app, inputs)
	if err != nil {
		return errors.New("Cannot prepare retained Compose runtime files.")
	}
	if err = progress(core.DeploymentStarting, "Starting retained Compose images on "+server.Name); err != nil {
		return err
	}
	if err = e.command(ctx, nil, io.Discard, "docker", "compose", "-p", dockerResourceName(app.ID), "-f", path, "up", "-d", "--no-build", "--pull", "never", "--remove-orphans", "--wait", "--wait-timeout", "120"); err != nil {
		return errors.New("Compose apply failed or readiness timed out. Inspect the owned containers before retrying; retained images and volume data were preserved.")
	}
	if err = progress(core.DeploymentChecking, "Retained Compose services passed readiness checks"); err != nil {
		return err
	}
	return progress(core.DeploymentRouting, routeMessage(core.App{Domain: inputs.Domain}))
}

func validateComposeArtifact(inputs dockerArtifact, appID string) error {
	services, ok := inputs.Compose["services"].(map[string]any)
	if !ok || len(services) == 0 || len(services) != len(inputs.Images) {
		return errors.New("Retained Compose services are incomplete.")
	}
	for name, item := range services {
		service, ok := item.(map[string]any)
		if !ok || !dockerImageID.MatchString(inputs.Images[name]) || service["image"] != inputs.Images[name] {
			return errors.New("Retained Compose images do not match the service definitions.")
		}
		labels, ok := service["labels"].(map[string]any)
		if !ok || labels["dispatch.app"] != appID {
			return errors.New("Retained Compose ownership is invalid.")
		}
		if service["build"] != nil || service["develop"] != nil {
			return errors.New("Retained Compose releases cannot build or develop images during restore.")
		}
	}
	if len(inputs.Files) > 4096 {
		return errors.New("Too many retained runtime files.")
	}
	for name, file := range inputs.Files {
		if _, err := within("/retained", filepath.FromSlash(name)); err != nil || name == "" || name == "." || filepath.IsAbs(name) || os.FileMode(file.Mode)&os.ModeSymlink != 0 {
			return errors.New("Invalid retained runtime file path.")
		}
	}
	for _, ref := range composeFilePaths(inputs.Compose) {
		key := "source"
		if ref["file"] != nil {
			key = "file"
		}
		path, _ := ref[key].(string)
		if relative, ok := strings.CutPrefix(path, retainedFilePrefix); ok {
			if _, found := inputs.Files[relative]; !found {
				return errors.New("A retained Compose runtime file is missing.")
			}
		}
	}
	return nil
}

// Profiles are selected at deploy time. Retain only those services so restore
// does not depend on the controller's future COMPOSE_PROFILES setting.
func filterComposeProfiles(config map[string]any) {
	enabled := strings.Split(os.Getenv("COMPOSE_PROFILES"), ",")
	services, _ := config["services"].(map[string]any)
	for name, item := range services {
		service, ok := item.(map[string]any)
		if !ok {
			continue
		}
		profiles, _ := service["profiles"].([]any)
		active := len(profiles) == 0
		for _, profile := range profiles {
			for _, selection := range enabled {
				if selection == "*" || profile == selection && selection != "" {
					active = true
				}
			}
		}
		if !active {
			delete(services, name)
		} else {
			delete(service, "profiles")
		}
	}
}
