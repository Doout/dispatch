package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"reflect"
	"slices"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func validateInputPath(value string) error {
	if strings.TrimSpace(value) == "" || path.IsAbs(value) || strings.ContainsAny(value, "\\\x00*?[") {
		return fmt.Errorf("input path %q must be a relative file or directory without wildcards", value)
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return fmt.Errorf("input path %q must not traverse parent directories", value)
		}
	}
	return nil
}

func jobInputPaths(job JobSpec, alias string) []string {
	paths := job.SourcePaths[alias]
	if len(paths) == 0 {
		return []string{"."}
	}
	result := make([]string, 0, len(paths))
	for _, p := range paths {
		result = append(result, path.Clean(p))
	}
	slices.Sort(result)
	return slices.Compact(result)
}

// Only sources explicitly scoped by a job use content matching. Other sources
// keep commit matching, including sources whose revision is used in templates.
func applicationInputPaths(spec ApplicationSpec) map[string][]string {
	result := map[string][]string{}
	jobs := make([]JobSpec, 0, len(spec.Jobs)+len(spec.Finally))
	for _, job := range spec.Jobs {
		jobs = append(jobs, job)
	}
	for _, job := range spec.Finally {
		jobs = append(jobs, job)
	}
	for _, job := range jobs {
		for alias := range job.SourcePaths {
			result[alias] = []string{}
		}
	}
	for _, job := range jobs {
		for _, alias := range jobSourceAliases(job) {
			if _, ok := result[alias]; ok {
				result[alias] = append(result[alias], jobInputPaths(job, alias)...)
			}
		}
	}
	for _, deployment := range spec.Deployments {
		h := deployment.Helm
		if _, ok := result[h.SourceRef]; ok {
			result[h.SourceRef] = append(result[h.SourceRef], path.Clean(h.ChartPath))
		}
		for _, file := range h.ValuesFiles {
			if _, ok := result[file.SourceRef]; ok {
				result[file.SourceRef] = append(result[file.SourceRef], path.Clean(file.Path))
			}
		}
	}
	// Commit interpolation is an actual input even when file contents match.
	document := Document{Spec: &spec}
	contents, _ := document.MarshalYAML()
	for _, match := range templatePattern.FindAllStringSubmatch(string(contents), -1) {
		parts := strings.Split(strings.TrimSpace(match[1]), ".")
		if len(parts) == 3 && parts[0] == "sources" && parts[2] == "commit" {
			delete(result, parts[1])
		}
	}
	for alias, paths := range result {
		slices.Sort(paths)
		result[alias] = slices.Compact(paths)
	}
	return result
}

func (c *repositoryCache) contentHashes(ctx context.Context, repositoryURL, credentialID, branch, commit, base string, paths []string, environment []string) (map[string]string, error) {
	key := repositoryCacheKey(repositoryURL, credentialID)
	unlock := c.lock(key)
	defer unlock()
	mirror, err := c.prepareMirror(ctx, repositoryURL, key, branch, commit, environment)
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, value := range paths {
		if err := validateInputPath(value); err != nil {
			return nil, err
		}
		if base != "" {
			if err := validateInputPath(base); err != nil {
				return nil, err
			}
		}
		relative := path.Join(base, value)
		args := []string{"--git-dir", mirror, "ls-tree", "-z", commit, "--", ":(literal)" + relative}
		if relative == "." {
			args = []string{"--git-dir", mirror, "rev-parse", commit + "^{tree}"}
		}
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = environment
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("hash source input %s: %w", relative, err)
		}
		if len(output) == 0 {
			return nil, fmt.Errorf("source input %s does not exist at revision %s", relative, commit)
		}
		// Scoped symlinks could read an untracked target outside the selected input.
		if strings.HasPrefix(string(output), "120000 ") {
			return nil, fmt.Errorf("source input %s is a symlink; select its containing directory and target instead", relative)
		}
		digest := sha256.Sum256(output)
		result[path.Clean(value)] = "sha256:" + hex.EncodeToString(digest[:])
	}
	return result, nil
}

func (s *Service) hashSourceInputs(ctx context.Context, config core.ConfigSource, source core.WorkflowSourceRevision, paths []string) (map[string]string, error) {
	access, err := s.repositoryAccess(ctx, config, source.Repository)
	if err != nil {
		return nil, err
	}
	defer access.cleanup()
	if s.Repositories == nil {
		return nil, errors.New("repository cache is not configured")
	}
	return s.Repositories.contentHashes(ctx, access.url, access.credentialID, source.Branch, source.CommitSHA, source.Path, paths, access.environment)
}

func sameSourceInput(a, b core.WorkflowSourceRevision) bool {
	if a.Repository != b.Repository || a.Branch != b.Branch || a.Path != b.Path || a.Alias != b.Alias {
		return false
	}
	if len(a.ContentHashes) > 0 && len(b.ContentHashes) > 0 {
		return reflect.DeepEqual(a.ContentHashes, b.ContentHashes)
	}
	return a.CommitSHA == b.CommitSHA
}

// Project the persisted source snapshot onto this job's inputs. Helm values and
// another job's inputs must not invalidate a matching build.
func jobContentSources(job JobSpec, sources map[string]core.WorkflowSourceRevision) map[string]core.WorkflowSourceRevision {
	result := make(map[string]core.WorkflowSourceRevision, len(sources))
	for alias, source := range sources {
		if len(source.ContentHashes) > 0 {
			selected := map[string]string{}
			for _, p := range jobInputPaths(job, alias) {
				if hash, ok := source.ContentHashes[p]; ok {
					selected[p] = hash
				}
			}
			if len(selected) == len(jobInputPaths(job, alias)) {
				source.CommitSHA = ""
				source.ContentHashes = selected
			} else {
				source.ContentHashes = nil
			}
		}
		result[alias] = source
	}
	return result
}

// When path inputs are first enabled, verify old successful builds against the
// selected files rather than throwing their outputs away. Definition, secrets,
// and every unscoped source must still match before any Git lookup.
func (r *jobRuntime) findContentMatch(ctx context.Context, resource core.WorkflowResource, name string, job JobSpec, current map[string]core.WorkflowSourceRevision, secrets map[string]string, fingerprint string) (core.WorkflowJobResult, error) {
	candidates, err := r.service.Store.ListReusableWorkflowJobResults(ctx, resource.ID, name)
	if err != nil {
		return core.WorkflowJobResult{}, err
	}
	legacy := job
	legacy.SourcePaths = nil
	for _, candidate := range candidates {
		if !completeOutputs(candidate.Outputs, job.Outputs) || len(candidate.Sources) != len(current) {
			continue
		}
		if candidate.Fingerprint != jobExecutionFingerprint(name, legacy, candidate.Sources, r.inputs, secrets) {
			continue
		}
		compatible := true
		for alias, old := range candidate.Sources {
			now, ok := current[alias]
			if !ok || old.Repository != now.Repository || old.Branch != now.Branch || old.Path != now.Path || (len(now.ContentHashes) == 0 && old.CommitSHA != now.CommitSHA) {
				compatible = false
				break
			}
		}
		if !compatible {
			continue
		}
		hydrated := make(map[string]core.WorkflowSourceRevision, len(candidate.Sources))
		for alias, old := range candidate.Sources {
			if len(current[alias].ContentHashes) > 0 {
				old.ContentHashes, err = r.service.hashSourceInputs(ctx, r.source, old, jobInputPaths(job, alias))
				if err != nil {
					compatible = false
					break
				}
			}
			hydrated[alias] = old
		}
		if compatible && jobExecutionFingerprint(name, job, hydrated, r.inputs, secrets) == fingerprint {
			return candidate, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return core.WorkflowJobResult{}, err
	}
	return core.WorkflowJobResult{}, store.ErrNotFound
}

// Adding input declarations changes scheduling, not the application to deploy.
// Compare with the old digest so enabling this feature does not redeploy every
// slot solely to store its first content hashes.
func addsOnlyInputPaths(resource core.WorkflowResource, previousDigest string) bool {
	documents, err := Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].Spec == nil {
		return false
	}
	doc := documents[0]
	scoped := false
	for name, job := range doc.Spec.Jobs {
		if len(job.SourcePaths) > 0 {
			scoped = true
		}
		job.SourcePaths = nil
		doc.Spec.Jobs[name] = job
	}
	for name, job := range doc.Spec.Finally {
		if len(job.SourcePaths) > 0 {
			scoped = true
		}
		job.SourcePaths = nil
		doc.Spec.Finally[name] = job
	}
	if !scoped {
		return false
	}
	digest, err := doc.Digest()
	return err == nil && digest == previousDigest
}
