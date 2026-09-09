package workflow

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

const (
	maxJobLogBytes = 1 << 20
	maxJobOutput   = 64 << 10
)

// References include both the builder and waiters so a key is removed only
// after its last user exits. Waiting does not prevent cancellation.
type buildLock struct {
	token chan struct{}
	users int
}

func (s *Service) lockBuild(ctx context.Context, key string) (func(), error) {
	s.mu.Lock()
	if s.buildLocks == nil {
		s.buildLocks = map[string]*buildLock{}
	}
	gate := s.buildLocks[key]
	if gate == nil {
		gate = &buildLock{token: make(chan struct{}, 1)}
		s.buildLocks[key] = gate
	}
	gate.users++
	s.mu.Unlock()
	releaseReference := func() {
		s.mu.Lock()
		gate.users--
		if gate.users == 0 {
			delete(s.buildLocks, key)
		}
		s.mu.Unlock()
	}
	select {
	case gate.token <- struct{}{}:
		return func() { <-gate.token; releaseReference() }, nil
	case <-ctx.Done():
		releaseReference()
		return nil, ctx.Err()
	}
}

type jobRuntime struct {
	service   *Service
	source    core.ConfigSource
	revision  core.WorkflowRevision
	root      string
	paths     map[string]string
	inputs    map[string]string
	worktrees []*cachedWorktree
}

func (r *jobRuntime) executeJobs(ctx context.Context, resource core.WorkflowResource, jobs, final map[string]JobSpec, pipeline bool) (map[string]map[string]string, error) {
	outputs := map[string]map[string]string{}
	var runErr error
	for _, name := range sortedResourceNames(jobs) {
		job := jobs[name]
		values, err := r.executeJob(ctx, resource, name, job, pipeline)
		if err != nil {
			runErr = fmt.Errorf("job %s: %w", name, err)
			break
		}
		outputs[name] = values
	}
	for _, name := range sortedResourceNames(final) {
		job := final[name]
		job.Reuse = "never"
		values, err := r.executeJob(ctx, resource, "finally/"+name, job, pipeline)
		if err != nil && runErr == nil {
			runErr = fmt.Errorf("finally job %s: %w", name, err)
		}
		outputs["finally/"+name] = values
	}
	return outputs, runErr
}

func (r *jobRuntime) executeJob(ctx context.Context, resource core.WorkflowResource, name string, job JobSpec, pipeline bool) (map[string]string, error) {
	relevant := jobSourceAliases(job)
	jobSources := make(map[string]core.WorkflowSourceRevision, len(relevant))
	for _, alias := range relevant {
		revision, ok := r.revision.Sources[alias]
		if !ok {
			return nil, fmt.Errorf("source %s is missing from the revision", alias)
		}
		jobSources[alias] = revision
	}
	secrets, err := r.resolveSecrets(ctx, job.Secrets)
	if err != nil {
		return nil, err
	}
	fingerprint := jobExecutionFingerprint(name, job, jobSources, r.inputs, secrets)
	if job.Reuse == "onInputMatch" && !pipeline {
		unlock, err := r.service.lockBuild(ctx, resource.ConfigSourceID+":"+fingerprint)
		if err != nil {
			return nil, err
		}
		defer unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		previous, err := r.service.Store.FindWorkflowJobResult(ctx, resource.ID, name, fingerprint)
		if err == nil && previous.State == "succeeded" && completeOutputs(previous.Outputs, job.Outputs) {
			now := time.Now().UTC()
			result := core.WorkflowJobResult{ID: ulid.Make().String(), ResourceID: resource.ID, RevisionID: r.revision.ID, JobName: name,
				Fingerprint: fingerprint, ReusedFromID: previous.ID, State: "succeeded", Sources: jobSources, Outputs: previous.Outputs,
				Log: "Reused matching job result " + previous.ID, CreatedAt: now, StartedAt: &now, FinishedAt: &now}
			if err := r.service.Store.CreateWorkflowJobResult(ctx, result); err != nil {
				return nil, err
			}
			return previous.Outputs, nil
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	started := time.Now().UTC()
	result := core.WorkflowJobResult{ID: ulid.Make().String(), ResourceID: resource.ID, RevisionID: r.revision.ID, JobName: name,
		Fingerprint: fingerprint, State: "running", Sources: jobSources, Outputs: map[string]string{}, CreatedAt: started, StartedAt: &started}
	if err := r.service.Store.CreateWorkflowJobResult(ctx, result); err != nil {
		return nil, err
	}
	outputs, logText, err := r.runJobCommand(ctx, job, secrets)
	finished := time.Now().UTC()
	result.Log, result.FinishedAt = logText, &finished
	if err != nil {
		result.State, result.Error = "failed", err.Error()
		_ = r.service.Store.UpdateWorkflowJobResult(context.Background(), result)
		return nil, err
	}
	result.State, result.Outputs = "succeeded", outputs
	if err := r.service.Store.UpdateWorkflowJobResult(ctx, result); err != nil {
		return nil, err
	}
	return outputs, nil
}

func (r *jobRuntime) runJobCommand(ctx context.Context, job JobSpec, secrets map[string]string) (map[string]string, string, error) {
	aliases := jobSourceAliases(job)
	for _, alias := range aliases {
		if _, err := r.checkout(ctx, alias); err != nil {
			return nil, "", err
		}
	}
	jobRevision := r.revision
	jobRevision.Sources = map[string]core.WorkflowSourceRevision{}
	jobPaths := map[string]string{}
	for _, alias := range aliases {
		jobRevision.Sources[alias] = r.revision.Sources[alias]
		jobPaths[alias] = r.paths[alias]
	}
	command, err := renderRuntime(job.Run, jobPaths, jobRevision.Sources, r.inputs, nil)
	if err != nil {
		return nil, "", err
	}
	cwd := r.paths[job.RunFrom]
	if cwd == "" {
		return nil, "", fmt.Errorf("source %s was not checked out", job.RunFrom)
	}
	outputPath := filepath.Join(r.root, "output-"+safePathPart(job.RunFrom)+"-"+ulid.Make().String()+".env")
	environment := workflowEnvironment(jobRevision, jobPaths, outputPath)
	for name, value := range secrets {
		environment = append(environment, name+"="+value)
	}
	var logs limitedBuffer
	logs.limit = maxJobLogBytes
	cmd := exec.CommandContext(ctx, "/bin/sh", "-eu", "-c", command)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = cwd, environment, &logs, &logs
	err = cmd.Run()
	logText := logs.String()
	if err != nil {
		return nil, logText, fmt.Errorf("command failed: %w", err)
	}
	outputs, err := readJobOutputs(outputPath, job.Outputs)
	if err != nil {
		return nil, logText, err
	}
	return outputs, logText, nil
}

func (r *jobRuntime) resolveSecrets(ctx context.Context, bindings map[string]SecretBinding) (map[string]string, error) {
	if len(bindings) == 0 {
		return map[string]string{}, nil
	}
	if r.service.Secrets == nil {
		return nil, errors.New("secret resolution is not configured")
	}
	secrets, err := r.service.Store.ListSecrets(ctx)
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for environment, binding := range bindings {
		id := ""
		for _, secret := range secrets {
			if secret.ID == binding.SecretRef || strings.EqualFold(secret.Name, binding.SecretRef) {
				id = secret.ID
				break
			}
		}
		if id == "" {
			return nil, fmt.Errorf("secret %s was not found", binding.SecretRef)
		}
		value, err := r.service.Secrets.Resolve(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("resolve secret %s: %w", binding.SecretRef, err)
		}
		result[environment] = string(value)
		clear(value)
	}
	return result, nil
}

func (r *jobRuntime) checkout(ctx context.Context, alias string) (string, error) {
	if path := r.paths[alias]; path != "" {
		return path, nil
	}
	revision, ok := r.revision.Sources[alias]
	if !ok {
		return "", fmt.Errorf("unknown source %s", alias)
	}
	access, err := r.service.repositoryAccess(ctx, r.source, revision.Repository)
	if err != nil {
		return "", err
	}
	defer access.cleanup()
	root := filepath.Join(r.root, "sources", safePathPart(alias))
	cache := r.service.Repositories
	if cache == nil {
		cache = newRepositoryCache("")
		r.service.Repositories = cache
	}
	worktree, err := cache.checkout(ctx, access.url, access.credentialID, revision.Branch, revision.CommitSHA, root, access.environment)
	if err != nil {
		return "", fmt.Errorf("checkout source %s: %w", alias, err)
	}
	r.worktrees = append(r.worktrees, worktree)
	path := root
	if revision.Path != "" {
		path, err = safeJoin(root, revision.Path)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("source %s path %s is not a directory", alias, revision.Path)
		}
	}
	r.paths[alias] = path
	return path, nil
}

func (r *jobRuntime) close() {
	for index := len(r.worktrees) - 1; index >= 0; index-- {
		_ = r.worktrees[index].remove(context.Background())
	}
	r.worktrees = nil
}

func workflowEnvironment(revision core.WorkflowRevision, paths map[string]string, outputPath string) []string {
	environment := []string{"GIT_TERMINAL_PROMPT=0", "DISPATCH_REVISION_ID=" + revision.ID, "DISPATCH_OUTPUT_FILE=" + outputPath, "GITHUB_OUTPUT=" + outputPath}
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR", "DOCKER_HOST", "DOCKER_CONFIG"} {
		if value, ok := os.LookupEnv(name); ok {
			environment = append(environment, name+"="+value)
		}
	}
	for alias, source := range revision.Sources {
		prefix := "DISPATCH_SOURCE_" + strings.ToUpper(strings.ReplaceAll(alias, "-", "_"))
		environment = append(environment, prefix+"_PATH="+paths[alias], prefix+"_COMMIT="+source.CommitSHA, prefix+"_BRANCH="+source.Branch)
	}
	return environment
}

func jobFingerprint(name string, job JobSpec, sources map[string]core.WorkflowSourceRevision, inputs map[string]string) string {
	contents, _ := json.Marshal(struct {
		Format   string
		Platform string
		Name     string
		Job      JobSpec
		Sources  map[string]core.WorkflowSourceRevision
		Inputs   map[string]string
	}{Format: "shared-build-v1", Platform: runtime.GOOS + "/" + runtime.GOARCH, Name: name, Job: job, Sources: sources, Inputs: inputs})
	digest := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func jobExecutionFingerprint(name string, job JobSpec, sources map[string]core.WorkflowSourceRevision, inputs, secrets map[string]string) string {
	fingerprint := jobFingerprint(name, job, sources, inputs)
	if len(secrets) == 0 {
		return fingerprint
	}
	encoded, _ := json.Marshal(secrets)
	digest := sha256.Sum256(append([]byte(fingerprint), encoded...))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func completeOutputs(outputs map[string]string, declared []string) bool {
	for _, name := range declared {
		if _, ok := outputs[name]; !ok {
			return false
		}
	}
	return true
}

func jobSourceAliases(job JobSpec) []string {
	aliases := append([]string{job.RunFrom}, job.Sources...)
	slices.Sort(aliases)
	return uniqueStrings(aliases)
}

func readJobOutputs(path string, declared []string) (map[string]string, error) {
	if len(declared) == 0 {
		return map[string]string{}, nil
	}
	file, err := os.Open(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("job did not create DISPATCH_OUTPUT_FILE")
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxJobOutput+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxJobOutput {
		return nil, errors.New("job output exceeds 64 KiB")
	}
	outputs := map[string]string{}
	if json.Unmarshal(contents, &outputs) != nil {
		scanner := bufio.NewScanner(bytes.NewReader(contents))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
			name, value, ok := strings.Cut(line, "=")
			if !ok || strings.TrimSpace(name) == "" {
				return nil, errors.New("job output must contain JSON or name=value lines")
			}
			outputs[strings.TrimSpace(name)] = strings.Trim(strings.TrimSpace(value), "\"'")
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	for _, name := range declared {
		if _, ok := outputs[name]; !ok {
			return nil, fmt.Errorf("job output %s is missing", name)
		}
	}
	for name := range outputs {
		if !slices.Contains(declared, name) {
			delete(outputs, name)
		}
	}
	return outputs, nil
}

func renderRuntime(value string, paths map[string]string, sources map[string]core.WorkflowSourceRevision, inputs map[string]string, stage *StageSpec) (string, error) {
	var renderErr error
	rendered := templatePattern.ReplaceAllStringFunc(value, func(expression string) string {
		parts := strings.Split(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(expression, "{{"), "}}")), ".")
		switch {
		case len(parts) == 3 && parts[0] == "sources":
			source, ok := sources[parts[1]]
			if !ok {
				renderErr = fmt.Errorf("source %s is unavailable", parts[1])
				return ""
			}
			switch parts[2] {
			case "path":
				return paths[parts[1]]
			case "commit":
				return source.CommitSHA
			case "branch":
				return source.Branch
			}
		case len(parts) == 2 && parts[0] == "inputs":
			value, ok := inputs[parts[1]]
			if !ok {
				renderErr = fmt.Errorf("input %s is unavailable", parts[1])
				return ""
			}
			return value
		case len(parts) == 2 && parts[0] == "stage" && stage != nil:
			if parts[1] == "name" {
				return stage.Name
			}
			if parts[1] == "url" {
				return stage.URL
			}
		}
		renderErr = fmt.Errorf("template %s is unavailable", expression)
		return ""
	})
	return rendered, renderErr
}

func safeJoin(root, value string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if value == "." || value == "" {
		return root, nil
	}
	if filepath.IsAbs(value) || value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		return "", errors.New("source path must remain inside the repository")
	}
	joined := filepath.Join(root, value)
	relative, err := filepath.Rel(root, joined)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("source path must remain inside the repository")
	}
	return joined, nil
}

func safePathPart(value string) string {
	value = strings.Map(func(char rune) rune {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			return char
		}
		return '-'
	}, value)
	if value == "" {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return value
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
	full   bool
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
			b.full = true
		}
		_, _ = b.buffer.Write(value)
	} else {
		b.full = true
	}
	return written, nil
}

func (b *limitedBuffer) String() string {
	value := b.buffer.String()
	if b.full {
		value += "\n[log truncated]"
	}
	return value
}
