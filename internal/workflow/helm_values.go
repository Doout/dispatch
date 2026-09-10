package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"helm.sh/helm/v3/pkg/chartutil"
	helmvalues "helm.sh/helm/v3/pkg/cli/values"
)

// Merge files before inline runtime values. File contents belong to Helm's tpl,
// not the Pipeline expression renderer.
func (s *Service) deploymentValues(ctx context.Context, source core.ConfigSource, revision core.WorkflowRevision, stage StageSpec, spec HelmDeploymentSpec) (map[string]any, []string, error) {
	root, err := os.MkdirTemp("", "dispatch-values-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(root)
	runtime := &jobRuntime{service: s, source: source, revision: revision, root: root, paths: map[string]string{}}
	defer runtime.close()
	paths := []string{}
	evidence := []string{}
	origins := map[string]string{}
	for _, file := range spec.ValuesFiles {
		directory, err := runtime.checkout(ctx, file.SourceRef)
		if err != nil {
			return nil, nil, err
		}
		// Check against the repository root too, in case source.path is a symlink.
		repositoryRoot := filepath.Join(root, "sources", safePathPart(file.SourceRef))
		directory, err = containedPath(repositoryRoot, directory)
		if err != nil {
			return nil, nil, err
		}
		path, err := safeJoin(directory, file.Path)
		if err != nil {
			return nil, nil, err
		}
		path, err = containedPath(directory, path)
		if err != nil {
			return nil, nil, err
		}
		fileValues, digest, err := readHelmValues(path)
		if err != nil {
			return nil, nil, fmt.Errorf("values file %s:%s: %w", file.SourceRef, file.Path, err)
		}
		recordValueSources(origins, "", fileValues, file.SourceRef+":"+file.Path)
		paths = append(paths, path)
		evidence = append(evidence, fmt.Sprintf("Helm values %s@%s:%s sha256:%s", file.SourceRef, revision.Sources[file.SourceRef].CommitSHA, file.Path, digest))
	}
	inline, err := renderValues(spec.Values, revision.Sources, stage)
	if err != nil {
		return nil, nil, err
	}
	recordValueSources(origins, "", inline, "Inline values")
	raw, err := json.Marshal(inline)
	if err != nil {
		return nil, nil, err
	}
	inlinePath := filepath.Join(root, "inline.json")
	if err := os.WriteFile(inlinePath, raw, 0600); err != nil {
		return nil, nil, err
	}
	paths = append(paths, inlinePath)
	options := helmvalues.Options{ValueFiles: paths}
	values, err := options.MergeValues(nil)
	if err == nil && len(origins) > 0 {
		raw, _ := json.Marshal(origins)
		evidence = append(evidence, "Helm value sources: "+string(raw))
	}
	return values, evidence, err
}

func containedPath(root, path string) (string, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("values path must remain inside its source checkout")
	}
	return path, nil
}

func readHelmValues(path string) (map[string]any, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, "", fmt.Errorf("values file must be a regular file no larger than 4 MiB")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	values, err := chartutil.ReadValues(raw)
	if err != nil {
		return nil, "", fmt.Errorf("invalid YAML values")
	}
	return values, fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

// Record paths and their source, never the values themselves.
func recordValueSources(out map[string]string, path string, value any, source string) {
	if object, ok := value.(map[string]any); ok && len(object) > 0 {
		delete(out, path)
		for key, item := range object {
			next := key
			if path != "" {
				next = path + "." + key
			}
			recordValueSources(out, next, item, source)
		}
	} else if path != "" {
		for previous := range out {
			if strings.HasPrefix(previous, path+".") {
				delete(out, previous)
			}
		}
		out[path] = source
	}
}
