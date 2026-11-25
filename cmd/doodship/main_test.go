package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}
func gitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v %s", args[0], err, b)
	}
	return strings.TrimSpace(string(b))
}

func TestOfflineWorkflowRun(t *testing.T) {
	// A dirty checkout on a PR branch. The slot must still deploy the pinned main revision.
	repo := t.TempDir()
	gitFixture(t, repo, "init", "-q", "-b", "main")
	gitFixture(t, repo, "remote", "add", "origin", "https://offline.invalid/example/hello.git")
	chart := filepath.Join(repo, "chart")
	if err := os.Mkdir(chart, 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(chart, "Chart.yaml"), "apiVersion: v2\nname: hello\nversion: 1.0.0\n", 0600)
	put(t, filepath.Join(repo, "base.yaml"), "workers: 1\nworkers: 2\n_pipeline: {enabled: true}\n", 0600)
	put(t, filepath.Join(repo, "marker"), "slot", 0600)
	gitFixture(t, repo, "add", ".")
	gitFixture(t, repo, "commit", "-qm", "Slot")
	gitFixture(t, repo, "switch", "-qc", "feature")
	put(t, filepath.Join(repo, "marker"), "preview", 0600)
	gitFixture(t, repo, "commit", "-qam", "PR")
	put(t, filepath.Join(repo, "marker"), "uncommitted", 0600)
	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls")
	valuesPath := filepath.Join(bin, "values")
	basePath := filepath.Join(bin, "base-values")
	put(t, filepath.Join(bin, "helm"), fmt.Sprintf(`#!/bin/sh
set -eu
echo "$*" >> '%s'
case "$1" in
  version) printf '%%s\n' "$DOODSHIP_TEST_HELM_VERSION" ;;
  install|upgrade)
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "-f" ]; then
        case "$2" in
          */overrides.json) cp "$2" '%s' ;;
          *) cp "$2" '%s' ;;
        esac
      fi
      shift
    done ;;
esac
`, logPath, valuesPath, basePath), 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DISPATCH_URL", "http://127.0.0.1:1")
	t.Setenv("DOODSHIP_TEST_PASSWORD", "")
	t.Setenv("DOODSHIP_UNDECLARED", "unrelated-secret")
	envFile := filepath.Join(bin, "local.env")
	put(t, envFile, "DOODSHIP_TEST_PASSWORD=fixture-password\n", 0600)
	for _, kind := range []string{"slot", "preview"} {
		t.Run(kind, func(t *testing.T) {
			helmVersion := "v3.21.4"
			if kind == "preview" {
				helmVersion = "v4.3.0"
			}
			t.Setenv("DOODSHIP_TEST_HELM_VERSION", helmVersion)
			raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "doodship", kind+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			yaml := string(raw)
			start := strings.Index(yaml, "      run:")
			end := strings.Index(yaml, "      outputs:")
			yaml = yaml[:start] + `      run: |
        test -z "${DOODSHIP_UNDECLARED:-}"
        echo "$TOKEN"
        echo "message=$(cat marker)" >> "$DISPATCH_OUTPUT_FILE"
      secrets:
        TOKEN: {secretRef: DOODSHIP_TEST_PASSWORD}
` + yaml[end:]
			yaml = strings.Replace(yaml, "        chartPath: chart\n", "        chartPath: chart\n        valuesFiles: [{sourceRef: hello, path: base.yaml}]\n", 1)
			file := filepath.Join(bin, kind+".yaml")
			put(t, file, yaml, 0600)
			args := []string{"workflow", "run", "--file", file, "--source", "hello=" + repo, "--target", "local=test-context", "--non-interactive"}
			if kind == "preview" {
				args = append(args, "--pr", "42")
			}
			var log bytes.Buffer
			if err := run(t.Context(), append(args, "--dry-run"), &log, &log); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(log.String(), "Required binding: DOODSHIP_TEST_PASSWORD [missing]") {
				t.Fatal(log.String())
			}
			log.Reset()
			if err := run(t.Context(), args, &log, &log); err == nil || !strings.Contains(err.Error(), "missing bindings") {
				t.Fatalf("missing binding: %v", err)
			}
			if _, err := os.Stat(logPath); !os.IsNotExist(err) {
				t.Fatal("preflight invoked Helm")
			}
			log.Reset()
			if err := run(t.Context(), append(args, "--env-file", envFile), &log, &log); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(log.String(), "fixture-password") || !strings.Contains(log.String(), "[REDACTED]") {
				t.Fatal("job log did not redact binding")
			}
			b, err := os.ReadFile(valuesPath)
			if err != nil || string(b) != fmt.Sprintf(`{"message":"%s"}`, kind) {
				t.Fatalf("values: %s, %v", b, err)
			}
			b, err = os.ReadFile(basePath)
			if err != nil || string(b) != `{"workers":2}` {
				t.Fatalf("base values: %s, %v", b, err)
			}
			calls, _ := os.ReadFile(logPath)
			if strings.Contains(string(calls), " --all") != (kind == "slot") {
				t.Fatalf("unexpected release status flags for %s: %s", helmVersion, calls)
			}
			release := "hello-slot"
			if kind == "preview" {
				release = "hello-preview-42"
			}
			if !strings.Contains(string(calls), "install "+release+" ") || !strings.Contains(string(calls), "--kube-context test-context --namespace default") {
				t.Fatal(string(calls))
			}
			if err := os.Remove(logPath); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJobFailureStopsDeployment(t *testing.T) {
	repo := t.TempDir()
	gitFixture(t, repo, "init", "-q", "-b", "main")
	put(t, filepath.Join(repo, "marker"), "unchanged", 0600)
	gitFixture(t, repo, "add", ".")
	gitFixture(t, repo, "commit", "-qm", "Fixture")
	file := filepath.Join(repo, "workflow.yaml")
	put(t, file, `apiVersion: dispatch/v1alpha1
kind: Application
metadata: {name: failure}
spec:
  sources: {app: {repository: example/app}}
  jobs:
    broken: {runFrom: app, run: "exit 17"}
    later: {runFrom: app, needs: [broken], run: "echo wrong"}
  finally:
    cleanup: {runFrom: app, run: "echo cleanup-ran"}
`, 0600)
	var log bytes.Buffer
	err := run(t.Context(), []string{"workflow", "run", "--file", file, "--source", "app=" + repo}, &log, &log)
	if err == nil || !strings.Contains(err.Error(), "broken") || !strings.Contains(log.String(), "cleanup-ran") || strings.Contains(log.String(), "Running job later") {
		t.Fatalf("%v: %s", err, log.String())
	}
}

func TestListInputsWithOnlyWorkflowFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "slot.yaml")
	put(t, file, `apiVersion: dispatch/v1alpha1
kind: Application
metadata: {name: demo}
spec:
  sources: {app: {repository: unavailable/app}}
  jobs:
    build:
      runFrom: app
      run: exit 77
      secrets:
        LOGIN: {secretRef: CREDENTIALS, key: user}
        PASSWORD: {secretRef: CREDENTIALS, key: absent}
        MODE: {secretRef: MODE}
  finally:
    cleanup:
      runFrom: app
      run: exit 88
      secrets: {KEY: {secretRef: CLEANUP_KEY}, FLAG: {secretRef: FLAG}}
`, 0600)
	envfile := filepath.Join(dir, "job.env")
	put(t, envfile, "CREDENTIALS={\"user\":\"private-user\"}\nMODE=overridden-file-value\n", 0600)
	t.Setenv("CLEANUP_KEY", "")
	t.Setenv("FLAG", "secret-env-value")
	t.Setenv("KUBECONFIG", "sensitive-path")
	var log bytes.Buffer
	example := filepath.Join(dir, ".env.example")
	args := []string{"workflow", "run", "--file", file, "--list-inputs", "--env-file", envfile, "--var", "MODE=secret-flag-value", "--write-env-example", example}
	err := run(t.Context(), args, &log, &log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CREDENTIALS [set (env file)]", "build -> LOGIN (JSON key user)", "PASSWORD (JSON key absent) [unavailable]", "MODE [set (--var)]", "FLAG [set (environment)]", "CLEANUP_KEY [missing]", "finally/cleanup -> KEY", "KUBECONFIG", "DISPATCH_SOURCE_APP_COMMIT", "GITHUB_OUTPUT"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("missing %q in %s", want, log.String())
		}
	}
	for _, value := range []string{"private-user", "secret-env-value", "secret-flag-value", "overridden-file-value", "sensitive-path"} {
		if strings.Contains(log.String(), value) {
			t.Error("inspection printed a value")
		}
	}
	raw, err := os.ReadFile(example)
	if err != nil {
		t.Fatal(err)
	}
	var assignments []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			assignments = append(assignments, line)
		}
	}
	if got := strings.Join(assignments, ","); got != "CLEANUP_KEY=,CREDENTIALS=,FLAG=,MODE=" {
		t.Fatalf("unexpected template assignments: %q", got)
	}
	for _, s := range []string{"private-user", "secret-env-value", "secret-flag-value", "overridden-file-value", "sensitive-path"} {
		if strings.Contains(string(raw), s) {
			t.Fatal("template included a value")
		}
	}
	if !strings.Contains(string(raw), "JSON key absent") {
		t.Fatal("missing JSON key guidance")
	}
	filled := strings.ReplaceAll(string(raw), "CREDENTIALS=\n", "CREDENTIALS={\"user\":\"demo\",\"absent\":\"demo\"}\n")
	put(t, example, filled, 0600)
	if err := run(t.Context(), args, &log, &log); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected existing file error: %v", err)
	}
	preserved, err := os.ReadFile(example)
	if err != nil || string(preserved) != filled {
		t.Fatal("existing file changed")
	}
	log.Reset()
	if err := run(t.Context(), []string{"workflow", "run", "--file", file, "--list-inputs", "--env-file", example}, &log, &log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "CREDENTIALS [set (env file)]") || strings.Contains(log.String(), "[unavailable]") {
		t.Fatal(log.String())
	}

}

func TestAutomationSubcommand(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if code := execute(t.Context(), []string{"automation", "--help"}, strings.NewReader(""), &out, &diagnostics); code != 0 || !strings.Contains(diagnostics.String(), "auth status") {
		t.Fatalf("automation help failed with %d: %s", code, &diagnostics)
	}
	t.Setenv("DOODSHIP_TOKEN_FILE", filepath.Join(t.TempDir(), "missing"))
	diagnostics.Reset()
	if code := execute(t.Context(), []string{"automation", "auth", "status"}, strings.NewReader(""), &out, &diagnostics); code != 2 || !strings.Contains(out.String(), `"code":"configuration"`) {
		t.Fatalf("automation did not preserve its JSON failure and exit code: %d %s", code, &out)
	}
}
