package deploy

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestDockerComposeRollbackIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_ROLLBACK_DOCKER_INTEGRATION") != "1" {
		t.Skip("set DISPATCH_ROLLBACK_DOCKER_INTEGRATION=1 for disposable local containers")
	}
	for _, build := range []core.BuildType{core.BuildTypeDockerfile, core.BuildTypeCompose} {
		t.Run(string(build), func(t *testing.T) {
			data, e, app, server, _ := runtimeFixture(t, build)
			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()
			app.ID = "rollback-" + strings.ToLower(ulid.Make().String())
			app.Name = app.ID
			repo := t.TempDir()
			files := map[string]string{"Dockerfile": "FROM busybox:1.37\nCOPY config/version /version\nCMD [\"sleep\",\"300\"]\n", "config/version": "first"}
			files["compose.yaml"] = "services:\n  api:\n    image: busybox:1.37\n    environment:\n      APIKEY: 'literal$$APIKEY'\n    command: [sh, -c, 'cat /config/version > /version; test -f /data/protected || echo persisted > /data/protected; sleep 300']\n    volumes:\n      - type: bind\n        source: ./config\n        target: /config\n        read_only: true\n      - data:/data\nvolumes:\n  data: {}\n"
			serviceFixtureRepo(t, repo, files)
			app.SourceRepo = "file://" + repo
			app.Branch = "main"
			app.ContextPath = "."
			app.DockerfilePath = "Dockerfile"
			app.ComposePath = "compose.yaml"
			if err := data.CreateApp(ctx, app); err != nil {
				t.Fatal(err)
			}
			docker := func(args ...string) string {
				t.Helper()
				out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
				if err != nil {
					t.Fatalf("docker %s: %s %v", args, out, err)
				}
				return strings.TrimSpace(string(out))
			}
			gitRevision := func() string {
				t.Helper()
				cmd := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "HEAD")
				out, err := cmd.Output()
				if err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(string(out))
			}
			name := dockerResourceName(app.ID)
			if build == core.BuildTypeCompose {
				name += "-api-1"
			}
			t.Cleanup(func() {
				cleanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := e.Cleanup(cleanCtx, app, server, func(core.DeploymentState, string) error { return nil }); err != nil {
					t.Errorf("cleanup: %v", err)
				}
				if build == core.BuildTypeCompose {
					if out, err := exec.Command("docker", "volume", "rm", dockerResourceName(app.ID)+"_data").CombinedOutput(); err != nil {
						t.Errorf("test volume cleanup: %s %v", out, err)
					}
				}
			})
			var commands []string
			e.run = func(ctx context.Context, in io.Reader, out io.Writer, bin string, args ...string) error {
				commands = append(commands, bin+" "+strings.Join(args, " "))
				return command(ctx, in, out, bin, args...)
			}
			s := NewService(data, SnapshotExecutor{Store: data, Next: e})
			s.ConfigureRuntimeRollback(e)
			deploy := func() core.Deployment {
				t.Helper()
				d, err := s.Start(ctx, app.ID, gitRevision())
				if err != nil {
					t.Fatal(err)
				}
				finished := waitRuntimeDeployment(t, data, d.ID)
				if finished.State != core.DeploymentSucceeded {
					t.Fatalf("deployment failed: %s", finished.Message)
				}
				return finished
			}
			first := deploy()
			if got := docker("exec", name, "cat", "/version"); got != "first" {
				t.Fatalf("first release: %s", got)
			}
			if err := os.WriteFile(filepath.Join(repo, "config", "version"), []byte("second"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, "git", "-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-am", "Change runtime fixture")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fixture commit: %s %v", out, err)
			}
			second := deploy()
			if got := docker("exec", name, "cat", "/version"); got != "second" {
				t.Fatalf("second release: %s", got)
			}
			review, err := s.PreviewRollback(ctx, first.ID)
			if err != nil || !review.Available {
				t.Fatalf("rollback unavailable: %+v %v", review, err)
			}
			docker("stop", name)
			if _, err = s.StartRollback(ctx, first.ID, second.ID, review.ReviewDigest, "operator", nil); err == nil {
				t.Fatal("external runtime state change accepted")
			}
			docker("start", name)
			review, err = s.PreviewRollback(ctx, first.ID)
			if err != nil {
				t.Fatal(err)
			}
			commands = nil
			rollback, err := s.StartRollback(ctx, first.ID, second.ID, review.ReviewDigest, "operator", nil)
			if err != nil {
				t.Fatal(err)
			}
			result := waitRuntimeDeployment(t, data, rollback.ID)
			if result.State != core.DeploymentSucceeded {
				t.Fatalf("restore failed: %s", result.Message)
			}
			for _, call := range commands {
				if strings.HasPrefix(call, "git ") || strings.Contains(call, "docker build ") || strings.Contains(call, " compose build ") {
					t.Fatalf("rollback rebuilt or fetched sources: %s", call)
				}
			}
			if got := docker("exec", name, "cat", "/version"); got != "first" {
				t.Fatalf("retained runtime not restored: %s", got)
			}
			artifact, err := data.GetRuntimeArtifact(ctx, rollback.ID)
			if err != nil || artifact.ScopeID != first.ID {
				t.Fatalf("artifact not inherited: %+v %v", artifact, err)
			}
			if build == core.BuildTypeCompose {
				if got := docker("exec", name, "sh", "-c", "printf '%s' \"$APIKEY\""); got != "literal$APIKEY" {
					t.Fatalf("literal environment changed: %q", got)
				}
				if got := docker("exec", name, "cat", "/data/protected"); got != "persisted" {
					t.Fatal("volume data changed during restore")
				}
				if err = e.Cleanup(ctx, app, server, func(core.DeploymentState, string) error { return nil }); err != nil {
					t.Fatal(err)
				}
				docker("volume", "inspect", dockerResourceName(app.ID)+"_data")
			}
			t.Logf("%s restored %s in a new deployment %s; database volume data preserved", build, first.ID, result.ID)
		})
	}
}
