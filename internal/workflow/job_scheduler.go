package workflow

import (
	"context"
	"os"
	"path/filepath"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowfile"
)

func validateJobConcurrency(limit int) error {
	return workflowfile.ValidateJobConcurrency(limit)
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
	limit := r.maxParallelJobs
	if limit == 0 {
		if pipeline {
			limit = 1
		} else {
			limit = 2
		}
	}
	return workflowfile.RunJobs(ctx, jobs, limit, func(ctx context.Context, name string, job JobSpec) (map[string]string, error) {
		runtime := r
		if limit > 1 {
			var err error
			runtime, err = r.isolatedJob(name)
			if err != nil {
				return nil, err
			}
			defer runtime.close()
		}
		return runtime.executeJob(ctx, resource, name, job, pipeline)
	})
}
