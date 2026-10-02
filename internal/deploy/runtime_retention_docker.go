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

type RuntimeRetentionBackend interface {
	InspectRetention(context.Context, core.App, core.Server) ([]core.RuntimeRetentionItem, error)
	PruneRetention(context.Context, core.App, core.Server, core.RuntimeRetentionItem) (core.RuntimeRetentionOutcome, error)
}
type runtimeArtifactInventory interface {
	ListRuntimeArtifacts(context.Context, string) ([]core.RuntimeArtifact, error)
	RetireRuntimeArtifact(context.Context, core.RuntimeArtifact) error
}

var retentionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var retentionContainerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

type retentionContainer struct {
	ID         string                    `json:"id"`
	Image      string                    `json:"image"`
	App        string                    `json:"app"`
	Deployment string                    `json:"deployment"`
	Running    bool                      `json:"running"`
	Paused     bool                      `json:"paused"`
	Restarting bool                      `json:"restarting"`
	Mounts     []struct{ Source string } `json:"mounts"`
}
type retentionImage struct {
	ID      string    `json:"id"`
	Tags    []string  `json:"tags"`
	Created time.Time `json:"created"`
}
type retentionOutput struct{ strings.Builder }

func (w *retentionOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > 1<<20 {
		return 0, errors.New("runtime inventory exceeds its output limit")
	}
	return w.Builder.Write(p)
}
func (e DockerExecutor) retentionCommand(ctx context.Context, args ...string) (string, error) {
	var out retentionOutput
	err := e.command(ctx, nil, &out, "docker", args...)
	return out.String(), err
}
func (e DockerExecutor) retentionContainers(ctx context.Context) ([]retentionContainer, error) {
	raw, err := e.retentionCommand(ctx, "ps", "--all", "--no-trunc", "--format", "{{.ID}}")
	if err != nil {
		return nil, errors.New("Cannot inspect containers for retention")
	}
	ids := strings.Fields(raw)
	if len(ids) > 1000 {
		return nil, errors.New("Container inventory exceeds the retention safety limit")
	}
	sort.Strings(ids)
	out := []retentionContainer{}
	for _, id := range ids {
		if !retentionContainerID.MatchString(id) {
			return nil, errors.New("Invalid immutable container identity")
		}
		raw, err = e.retentionCommand(ctx, "inspect", "--format", `{"id":{{json .Id}},"image":{{json .Image}},"app":{{json (index .Config.Labels "dispatch.app")}},"deployment":{{json (index .Config.Labels "dispatch.deployment")}},"running":{{json .State.Running}},"paused":{{json .State.Paused}},"restarting":{{json .State.Restarting}},"mounts":{{json .Mounts}}}`, id)
		var c retentionContainer
		if err != nil || json.Unmarshal([]byte(raw), &c) != nil || c.ID != id {
			return nil, errors.New("Container inventory changed during retention inspection")
		}
		out = append(out, c)
	}
	return out, nil
}
func (e DockerExecutor) retentionArtifacts(ctx context.Context, server core.Server) ([]core.RuntimeArtifact, error) {
	data, ok := e.Artifacts.(runtimeArtifactInventory)
	if !ok || e.Vault == nil {
		return nil, errors.New("Runtime artifact inventory is unavailable")
	}
	artifacts, err := data.ListRuntimeArtifacts(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	for i := range artifacts {
		a := &artifacts[i]
		if a.ServerID != server.ID || !retentionID.MatchString(a.AppID) || !retentionID.MatchString(a.DeploymentID) || !retentionID.MatchString(a.ScopeID) {
			return nil, errors.New("Runtime artifact ownership is invalid")
		}
		if !a.Metadata.Retired {
			raw, err := e.Vault.Decrypt(runtimeArtifactScope(*a), a.Ciphertext)
			if err != nil {
				return nil, errors.New("Cannot verify retained runtime inputs")
			}
			var inputs dockerArtifact
			valid := len(raw) <= maxRuntimeArtifactBytes && json.Unmarshal(raw, &inputs) == nil && inputs.Version == 1
			clear(raw)
			if !valid {
				return nil, errors.New("Retained runtime inputs are invalid")
			}
			a.Metadata.ProjectID = inputs.ProjectID
			a.Metadata.Images = inputs.Images
			a.Metadata.InputDigest = fmt.Sprintf("%x", sha256.Sum256([]byte(a.Ciphertext)))
		}
		if len(a.Metadata.Images) == 0 || len(a.Metadata.Images) > 1000 {
			return nil, errors.New("Retained image inventory is unavailable")
		}
		for _, id := range a.Metadata.Images {
			if !dockerImageID.MatchString(id) {
				return nil, errors.New("Invalid retained image identity")
			}
		}
	}
	return artifacts, nil
}
func artifactRetentionIdentity(a core.RuntimeArtifact) string {
	raw, _ := json.Marshal(struct {
		Deployment, App, Server, Scope, InputDigest string
		Images                                      map[string]string
	}{a.DeploymentID, a.AppID, a.ServerID, a.ScopeID, a.Metadata.InputDigest, a.Metadata.Images})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func (e DockerExecutor) InspectRetention(ctx context.Context, app core.App, server core.Server) ([]core.RuntimeRetentionItem, error) {
	if !localDockerServer(server) || app.ServerID != server.ID || app.BuildType != core.BuildTypeDockerfile && app.BuildType != core.BuildTypeCompose {
		return nil, errors.New("Runtime retention is unavailable for this target")
	}
	artifacts, err := e.retentionArtifacts(ctx, server)
	if err != nil {
		return nil, err
	}
	containers, err := e.retentionContainers(ctx)
	if err != nil {
		return nil, err
	}
	out := []core.RuntimeRetentionItem{}
	images := map[string]*core.RuntimeRetentionItem{}
	seenRevisions := map[string]bool{}
	for _, a := range artifacts {
		for _, id := range a.Metadata.Images {
			item := images[id]
			if item == nil {
				item = &core.RuntimeRetentionItem{Key: "image:" + server.ID + ":" + id, Kind: "image", ServerID: server.ID, AppID: app.ID, Name: id, Identity: id, CreatedAt: a.Metadata.CreatedAt, Protected: []string{}}
				images[id] = item
			}
			if a.Metadata.CreatedAt.After(item.CreatedAt) {
				item.CreatedAt = a.Metadata.CreatedAt
			}
			if a.Metadata.ProjectID != app.ProjectID {
				item.Protected = appendUnique(item.Protected, "Retained image belongs to another project")
			}
			if a.AppID != app.ID {
				item.Protected = appendUnique(item.Protected, "Shared with another application")
			}
			if !a.Metadata.Retired {
				item.Protected = appendUnique(item.Protected, "Referenced by retained runtime inputs")
			}
		}
		if a.AppID != app.ID {
			continue
		}
		seenRevisions[a.DeploymentID] = true
		if a.Metadata.Retired {
			continue
		}
		item := core.RuntimeRetentionItem{Key: "revision:" + server.ID + ":" + a.DeploymentID, Kind: "revision", ServerID: server.ID, AppID: app.ID, DeploymentID: a.DeploymentID, Name: a.DeploymentID, Identity: artifactRetentionIdentity(a), CreatedAt: a.Metadata.CreatedAt, Protected: []string{}, Containers: []string{}}
		if a.Metadata.ProjectID != app.ProjectID {
			item.Protected = appendUnique(item.Protected, "Retained runtime inputs belong to another project")
		}
		dir := e.artifactDirectory(app.ID, a.DeploymentID)
		for _, c := range containers {
			own := c.App == app.ID && c.Deployment == a.DeploymentID
			if own {
				item.Containers = append(item.Containers, c.ID)
				if c.Running || c.Paused || c.Restarting {
					item.Protected = appendUnique(item.Protected, "Container is running, paused, or restarting")
				}
				validImage := false
				for _, image := range a.Metadata.Images {
					validImage = validImage || c.Image == image
				}
				if !validImage {
					item.Protected = appendUnique(item.Protected, "Container image differs from retained revision")
				}
			}
			for _, m := range c.Mounts {
				if withinRetentionPath(dir, m.Source) && !own {
					item.Protected = appendUnique(item.Protected, "Retained configuration is mounted by another container")
				}
			}
		}
		out = append(out, item)
	}
	// A container without captured inputs must never be guessed from its name.
	for _, c := range containers {
		if c.App == app.ID && !seenRevisions[c.Deployment] {
			seenRevisions[c.Deployment] = true
			out = append(out, core.RuntimeRetentionItem{Key: "untracked:" + server.ID + ":" + c.ID, Kind: "revision", ServerID: server.ID, AppID: app.ID, DeploymentID: c.Deployment, Name: c.Deployment, Identity: c.ID, Protected: []string{"No verified retained runtime inputs"}})
		}
	}
	// Return only images this application's artifact index actually owns. References
	// from every other application on the target still protect shared images.
	ownedImages := map[string]bool{}
	for _, a := range artifacts {
		if a.AppID == app.ID {
			for _, id := range a.Metadata.Images {
				ownedImages[id] = true
			}
		}
	}
	rawImages, listErr := e.retentionCommand(ctx, "image", "ls", "--quiet", "--no-trunc")
	if listErr != nil {
		return nil, errors.New("Cannot confirm the image inventory")
	}
	presentImages := map[string]bool{}
	for _, id := range strings.Fields(rawImages) {
		if !dockerImageID.MatchString(id) {
			return nil, errors.New("Invalid image inventory identity")
		}
		presentImages[id] = true
	}
	if len(presentImages) > 10000 {
		return nil, errors.New("Image inventory exceeds the retention safety limit")
	}
	for id, item := range images {
		if !presentImages[id] {
			continue
		}
		if !ownedImages[id] {
			continue
		}
		raw, err := e.retentionCommand(ctx, "image", "inspect", "--format", `{"id":{{json .Id}},"tags":{{json .RepoTags}},"created":{{json .Created}}}`, id)
		var evidence retentionImage
		if err != nil { // A failed inspect alone is not evidence of absence.
			item.Protected = appendUnique(item.Protected, "Image is unavailable or could not be inspected")
		} else if json.Unmarshal([]byte(raw), &evidence) != nil || evidence.ID != id {
			return nil, errors.New("Image identity changed during retention inspection")
		} else {
			if evidence.Created.After(item.CreatedAt) {
				item.CreatedAt = evidence.Created
			}
			if len(evidence.Tags) == 0 {
				item.Protected = appendUnique(item.Protected, "No owned deployment image tag")
			}
			if len(evidence.Tags) > 1 {
				item.Protected = appendUnique(item.Protected, "Image has multiple tags")
			}
			for _, tag := range evidence.Tags {
				ownedTag := false
				for _, artifact := range artifacts {
					for _, image := range artifact.Metadata.Images {
						if image != id {
							continue
						}
						scope := strings.ToLower(artifact.ScopeID)
						if strings.HasPrefix(tag, "dispatch/") && !strings.HasPrefix(tag, "dispatch/build-cache:") && (strings.HasSuffix(tag, ":"+scope) || strings.HasPrefix(tag, "dispatch/compose-"+strings.ToLower(artifact.AppID)+":"+scope+"-")) {
							ownedTag = true
						}
					}
				}
				if !ownedTag {
					item.Protected = appendUnique(item.Protected, "External or build-cache image tag")
				}
			}
		}
		for _, c := range containers {
			if c.Image == id {
				item.Protected = appendUnique(item.Protected, "Image is referenced by a container")
			}
		}
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	if len(out) > 1000 {
		return nil, errors.New("Artifact inventory exceeds the review safety limit")
	}
	return out, nil
}
func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
func withinRetentionPath(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func (e DockerExecutor) PruneRetention(ctx context.Context, app core.App, server core.Server, item core.RuntimeRetentionItem) (core.RuntimeRetentionOutcome, error) {
	outcome := core.RuntimeRetentionOutcome{Key: item.Key, State: "failed", Message: "Runtime cleanup failed; inspect before retrying"}
	if item.AppID != app.ID || item.ServerID != server.ID || len(item.Protected) > 0 {
		return outcome, errors.New("Reviewed artifact ownership or protection does not authorize cleanup")
	}
	fresh, err := e.InspectRetention(ctx, app, server)
	if err != nil {
		return outcome, err
	}
	var current *core.RuntimeRetentionItem
	for i := range fresh {
		if fresh[i].Key == item.Key {
			current = &fresh[i]
			break
		}
	}
	if current == nil { // Only complete successful inventory can establish absence.
		outcome.State, outcome.Message = "absent", "Already absent; no runtime object was removed"
		return outcome, nil
	}
	if current.Identity != item.Identity || len(current.Protected) > 0 {
		outcome.State, outcome.Message = "protected", "Runtime references or identity changed after review"
		return outcome, nil
	}
	if item.Kind == "image" {
		if !dockerImageID.MatchString(item.Identity) {
			return outcome, errors.New("Invalid image identity")
		}
		_, err = e.retentionCommand(ctx, "image", "rm", item.Identity)
	} else if item.Kind == "revision" {
		approved := map[string]bool{}
		for _, id := range item.Containers {
			approved[id] = true
		}
		for _, id := range current.Containers {
			if !approved[id] {
				outcome.State, outcome.Message = "protected", "Revision gained a container after review"
				return outcome, nil
			}
		}
		for _, id := range current.Containers {
			if !retentionContainerID.MatchString(id) {
				return outcome, errors.New("Invalid container identity")
			}
			if _, err = e.retentionCommand(ctx, "rm", id); err != nil {
				return outcome, errors.New("Container could not be removed without force; retry after inspection")
			}
		}
		// Re-inspect all containers after removals before deleting materialized config.
		containers, checkErr := e.retentionContainers(ctx)
		if checkErr != nil {
			return outcome, checkErr
		}
		dir := e.artifactDirectory(app.ID, item.DeploymentID)
		for _, c := range containers {
			if c.App == app.ID && c.Deployment == item.DeploymentID {
				return outcome, errors.New("Revision still has a container")
			}
			for _, m := range c.Mounts {
				if withinRetentionPath(dir, m.Source) {
					return outcome, errors.New("Retained configuration is still mounted")
				}
			}
		}
		if err = removeRetainedDirectory(dir); err != nil {
			return outcome, err
		}
		a, getErr := e.Artifacts.GetRuntimeArtifact(ctx, item.DeploymentID)
		if getErr != nil {
			return outcome, getErr
		}
		artifacts, checkErr := e.retentionArtifacts(ctx, server)
		if checkErr != nil {
			return outcome, checkErr
		}
		for _, candidate := range artifacts {
			if candidate.DeploymentID == a.DeploymentID {
				a = candidate
				break
			}
		}
		if artifactRetentionIdentity(a) != item.Identity {
			return outcome, errors.New("Runtime inputs changed during cleanup")
		}
		if a.Metadata.CreatedAt.IsZero() || item.CreatedAt.After(a.Metadata.CreatedAt) {
			a.Metadata.CreatedAt = item.CreatedAt
		}
		err = e.Artifacts.(runtimeArtifactInventory).RetireRuntimeArtifact(ctx, a)
	} else {
		return outcome, errors.New("Unsupported runtime retention kind")
	}
	if err != nil {
		return outcome, errors.New("Runtime object could not be removed safely; retry the original review after inspection")
	}
	outcome.State, outcome.Message = "removed", "Removed the reviewed runtime artifact"
	return outcome, nil
}

// Walk every parent without following symlinks. Only the exact generated config
// directory is removed; application bind mounts, volumes and networks are excluded.
func removeRetainedDirectory(dir string) error {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for p := absolute; p != filepath.Dir(p); p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("Retained configuration path is not a real directory")
		}
	}
	return os.RemoveAll(absolute)
}

var _ io.Writer = (*retentionOutput)(nil)
