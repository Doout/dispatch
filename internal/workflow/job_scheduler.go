package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/doout/dispatch/internal/core"
)

func validateJobConcurrency(limit int) error {
	if limit < 0 || limit > 32 {
		return errors.New("spec.maxParallelJobs must be between 1 and 32 when set")
	}
	return nil
}

func validateJobDependencies(prefix string, jobs map[string]JobSpec) error {
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] {
			return fmt.Errorf("%s.%s.needs contains a dependency cycle", prefix, name)
		}
		if visited[name] {
			return nil
		}
		visiting[name] = true
		seen := map[string]bool{}
		for _, dependency := range jobs[name].Needs {
			if _, ok := jobs[dependency]; !ok || seen[dependency] {
				return fmt.Errorf("%s.%s.needs contains unknown or duplicate job %q", prefix, name, dependency)
			}
			seen[dependency] = true
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[name], visited[name] = false, true
		return nil
	}
	for _, name := range sortedResourceNames(jobs) {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

// Each concurrent job owns its checkout and credentials. Repository mirrors and
// the Docker daemon's layer cache remain shared across jobs and revisions.
func (r *jobRuntime) isolatedJob(name string) (*jobRuntime, error) {
	root := filepath.Join(r.root, "jobs", safePathPart(name))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &jobRuntime{service: r.service, source: r.source, revision: r.revision, root: root,
		paths: map[string]string{}, inputs: r.inputs}, nil
}

func (r *jobRuntime) executeMainJobs(ctx context.Context, resource core.WorkflowResource, jobs map[string]JobSpec, pipeline bool) (map[string]map[string]string, error) {
	outputs := map[string]map[string]string{}
	if err := validateJobDependencies("jobs", jobs); err != nil {
		return outputs, err
	}
	limit := r.maxParallelJobs
	if limit == 0 {
		limit = 2
		if pipeline {
			limit = 1
		}
	}
	if err := validateJobConcurrency(limit); err != nil {
		return outputs, err
	}
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type completion struct {
		name   string
		values map[string]string
		err    error
	}
	finished := make(chan completion, len(jobs))
	started, succeeded := map[string]bool{}, map[string]bool{}
	running := 0
	var runErr error
	for len(succeeded) < len(jobs) {
		if err := jobCtx.Err(); err != nil && runErr == nil {
			runErr = err
		}
		if runErr == nil {
			for _, name := range sortedResourceNames(jobs) {
				if running == limit {
					break
				}
				if started[name] {
					continue
				}
				ready := true
				for _, dependency := range jobs[name].Needs {
					if !succeeded[dependency] {
						ready = false
						break
					}
				}
				if !ready {
					continue
				}
				started[name], running = true, running+1
				go func(name string, job JobSpec) {
					runtime := r
					if limit > 1 {
						var err error
						runtime, err = r.isolatedJob(name)
						if err != nil {
							finished <- completion{name: name, err: err}
							return
						}
					}
					values, err := runtime.executeJob(jobCtx, resource, name, job, pipeline)
					if runtime != r {
						runtime.close()
					}
					finished <- completion{name: name, values: values, err: err}
				}(name, jobs[name])
			}
		}
		if running == 0 {
			break
		}
		result := <-finished
		running--
		if result.err != nil && runErr == nil {
			runErr = ctx.Err()
			if runErr == nil {
				runErr = fmt.Errorf("job %s: %w", result.name, result.err)
			}
			cancel()
		}
		if result.err == nil {
			succeeded[result.name] = true
			outputs[result.name] = result.values
		}
	}
	return outputs, runErr
}
