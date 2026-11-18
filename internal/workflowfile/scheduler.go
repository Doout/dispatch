package workflowfile

import (
	"context"
	"fmt"
	"maps"
	"slices"
)

// RunJobs schedules jobs whose dependencies have completed. A failed job cancels its peers.
func RunJobs(ctx context.Context, jobs map[string]JobSpec, limit int, run func(context.Context, string, JobSpec) (map[string]string, error)) (map[string]map[string]string, error) {
	if limit < 1 {
		return nil, fmt.Errorf("job concurrency must be positive")
	}
	outputs := map[string]map[string]string{}
	if err := ValidateJobDependencies("jobs", jobs); err != nil {
		return outputs, err
	}
	if err := ValidateJobConcurrency(limit); err != nil {
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
			for _, name := range slices.Sorted(maps.Keys(jobs)) {
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
					values, err := run(jobCtx, name, job)
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
