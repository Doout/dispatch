package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
)

func immutableCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// ConfigureSourceResolution resolves a live source before the accepted record is
// written. Simulation intentionally does not fetch user repositories.
func (s *Service) ConfigureSourceResolution(auth SourceAuthExecutor) {
	s.configureSourceResolution(auth, false)
}

// ConfigureHostedSourceResolution resolves source refs without controller Git
// credentials or configuration. Deployment execution still belongs to workers.
func (s *Service) ConfigureHostedSourceResolution(auth SourceAuthExecutor) {
	s.configureSourceResolution(auth, true)
}

func (s *Service) configureSourceResolution(auth SourceAuthExecutor, isolated bool) {
	s.resolveRevision = func(ctx context.Context, app core.App, revision string) (string, error) {
		if app.BuildType == core.BuildTypeCompose && strings.TrimSpace(app.ComposeContent) != "" {
			return "inline", nil
		}
		if app.BuildType == core.BuildTypeHelm && !gitBackedHelmChart(app) {
			return "chart", nil
		}
		if immutableCommit(revision) {
			return revision, nil
		}
		resolved, err := auth.Resolve(ctx, app)
		if err != nil {
			return "", err
		}
		executor := DockerExecutor{}
		if isolated {
			environment, cleanup, err := PrepareIsolatedGitEnvironment(resolved)
			if err != nil {
				return "", err
			}
			defer cleanup()
			executor.run = func(ctx context.Context, input io.Reader, output io.Writer, binary string, args ...string) error {
				return commandWithEnvironment(ctx, environment, input, output, binary, args...)
			}
		}
		return executor.resolveRevision(ctx, resolved, revision)
	}
}

func (e DockerExecutor) resolveRevision(ctx context.Context, app core.App, revision string) (string, error) {
	if immutableCommit(revision) {
		return revision, nil
	}
	if revision != "" && revision != "HEAD" && revision != "chart" {
		return "", &runtimecontract.Error{Code: runtimecontract.InvalidRequest, Action: runtimecontract.Deploy, Message: "A source revision must be a full commit ID, or HEAD for the configured branch."}
	}
	if !validGitSourceForExecution(app) {
		return "", errors.New("source acquisition requires a supported repository URL and scoped credential")
	}
	ref := "HEAD"
	if app.Branch != "" {
		ref = "refs/heads/" + app.Branch
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := e.gitOutput(ctx, app, "ls-remote", "--exit-code", "--", app.SourceRepo, ref)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("cannot resolve the configured source branch; check repository access and branch identity")
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == ref && immutableCommit(fields[0]) {
			return fields[0], nil
		}
	}
	return "", errors.New("repository did not return an immutable commit for the configured branch")
}

func (e DockerExecutor) checkoutRevision(ctx context.Context, app core.App, revision, workspace string) (string, error) {
	sha, err := e.resolveRevision(ctx, app, revision)
	if err != nil {
		return "", err
	}
	for _, args := range [][]string{
		{"init", "--quiet", workspace},
		{"-C", workspace, "remote", "add", "origin", app.SourceRepo},
		{"-C", workspace, "fetch", "--depth", "1", "origin", sha},
		{"-C", workspace, "checkout", "--detach", "--force", sha},
	} {
		if err := e.gitCommand(ctx, app, args...); err != nil {
			return "", fmt.Errorf("checkout accepted source revision: %w", err)
		}
	}
	actual, err := e.gitOutput(ctx, app, "-C", workspace, "rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(actual) != sha {
		return "", errors.New("checked-out source does not match the accepted revision")
	}
	return sha, nil
}

func (e DockerExecutor) gitOutput(ctx context.Context, app core.App, args ...string) (string, error) {
	var output gitOutputBuffer
	var err error
	if e.run != nil {
		err = e.run(ctx, nil, &output, "git", args...)
	} else {
		var environment []string
		var cleanup func()
		environment, cleanup, err = PrepareGitEnvironment(app)
		if err != nil {
			return "", err
		}
		defer cleanup()
		err = commandWithEnvironment(ctx, environment, nil, &output, "git", args...)
	}
	return output.String(), err
}

type gitOutputBuffer struct{ strings.Builder }

func (b *gitOutputBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64<<10 {
		return 0, io.ErrShortBuffer
	}
	return b.Builder.Write(p)
}
