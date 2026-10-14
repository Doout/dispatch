package installation

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurlBootstrap(t *testing.T) {
	script, err := filepath.Abs("../../web/public/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name, endpoint, dockerStatus string
		symlink                      bool
		wantError                    string
	}{
		{name: "local", endpoint: "unix:///var/run/docker.sock"},
		{name: "remote", endpoint: "tcp://remote:2376", wantError: "local Docker engine"},
		{name: "denied", endpoint: "unix:///var/run/docker.sock", dockerStatus: "1", wantError: "cannot access"},
		{name: "symlink", endpoint: "unix:///var/run/docker.sock", symlink: true, wantError: "symbolic link"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			scratch := t.TempDir()
			bin := filepath.Join(scratch, "bin")
			os.Mkdir(bin, 0700)
			stub := `#!/bin/sh
set -eu
case "$1" in
  context) printf '%s\n' "$TEST_ENDPOINT" ;;
  info) exit "${TEST_DOCKER_STATUS:-0}" ;;
  run) printf '%s\n' "$@" > "$HOME/args"; env > "$HOME/environment"; cat > "$HOME/input" ;;
  *) exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			if scenario.symlink {
				if err := os.Symlink(bin, filepath.Join(scratch, ".doodship")); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", scratch)
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("DOCKER_HOST", "")
			t.Setenv("DOCKER_CONTEXT", "")
			t.Setenv("DOODSHIP_IMAGE", "")
			t.Setenv("TEST_ENDPOINT", scenario.endpoint)
			t.Setenv("TEST_DOCKER_STATUS", scenario.dockerStatus)
			payload := `{"enrollmentToken":"secret-stays-on-stdin","remoteAccess":true}`
			command := exec.Command("sh", script)
			command.Stdin = strings.NewReader(payload)
			output, err := command.CombinedOutput()
			if scenario.wantError != "" {
				if err == nil || !strings.Contains(string(output), scenario.wantError) {
					t.Fatalf("unexpected failure: %s %v", output, err)
				}
				if _, err := os.Stat(filepath.Join(scratch, "args")); !os.IsNotExist(err) {
					t.Fatal("invalid configuration started Docker setup")
				}
				return
			}
			if err != nil {
				t.Fatalf("bootstrap failed: %s %v", output, err)
			}
			input, _ := os.ReadFile(filepath.Join(scratch, "input"))
			if string(input) != payload {
				t.Fatal("connection payload changed")
			}
			for _, file := range []string{"args", "environment"} {
				contents, _ := os.ReadFile(filepath.Join(scratch, file))
				if strings.Contains(string(contents), "secret-stays-on-stdin") {
					t.Fatal("connection token leaked into " + file)
				}
			}
			args, _ := os.ReadFile(filepath.Join(scratch, "args"))
			for _, expected := range []string{"--connection-stdin", "type=bind,src=" + scratch + "/.doodship,dst=" + scratch + "/.doodship", "--host-uid", "--host-gid", "ghcr.io/doout/dispatch:main"} {
				if !strings.Contains(string(args), expected) {
					t.Fatal("missing setup argument", expected)
				}
			}
			info, err := os.Stat(filepath.Join(scratch, ".doodship"))
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatal("installation directory is not private")
			}
		})
	}
}
