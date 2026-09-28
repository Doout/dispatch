package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/doout/dispatch/internal/core"
)

// Registry login/logout must not race with another job's credentials. Changing
// the client configuration does not clear BuildKit's cache on the Docker daemon.
func isolatedDockerConfig(root string) (string, error) {
	directory, err := os.MkdirTemp(root, "docker-config-")
	if err != nil {
		return "", err
	}
	original := os.Getenv("DOCKER_CONFIG")
	if original == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			_ = os.RemoveAll(directory)
			return "", err
		}
		original = filepath.Join(home, ".docker")
	}
	contents, err := os.ReadFile(filepath.Join(original, "config.json"))
	if err == nil {
		err = os.WriteFile(filepath.Join(directory, "config.json"), contents, 0o600)
		clear(contents)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.RemoveAll(directory)
		return "", err
	}
	return directory, nil
}

func (r *jobRuntime) builderCacheKey(job JobSpec) string {
	sources := map[string]string{}
	for _, alias := range jobSourceAliases(job) {
		sources[alias] = normalizeRepository(r.revision.Sources[alias].Repository)
	}
	encoded, _ := json.Marshal(struct {
		ConfigSource string
		RunFrom      string
		Sources      map[string]string
	}{r.source.ID, job.RunFrom, sources})
	return string(encoded)
}

func builderCacheScore(key string, builder core.Server) string {
	digest := sha256.Sum256([]byte(key + "\x00" + builder.ID))
	return hex.EncodeToString(digest[:])
}
