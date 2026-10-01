package agentruntime

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"time"
)

// CheckPrerequisites gates capability advertisement, not merely the first job.
// An enrolled identity without a working daemon must remain unready.
func CheckPrerequisites(ctx context.Context) error {
	return checkPrerequisites(ctx, func(ctx context.Context, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		return cmd.Run()
	})
}
func checkPrerequisites(ctx context.Context, run func(context.Context, string, ...string) error) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, cmd := range [][]string{{"docker", "info", "--format", "{{.ServerVersion}}"}, {"docker", "compose", "version", "--short"}, {"git", "--version"}} {
		if err := run(ctx, cmd[0], cmd[1:]...); err != nil {
			return errors.New("Docker daemon, Compose v2 and Git must be available before advertising target readiness")
		}
	}
	return ctx.Err()
}
