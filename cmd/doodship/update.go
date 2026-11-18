package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"

	"github.com/doout/dispatch/scripts"
)

// Tagged release builds set this with -ldflags.
var releaseVersion = "development"

func updateExecutable(ctx context.Context, executable string, out, log io.Writer) error {
	target, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("locate installed executable: %w", err)
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", scripts.InstallCLI, "install-cli.sh", "--update-executable", target, releaseVersion)
	cmd.Stdout, cmd.Stderr = out, log
	return cmd.Run()
}
