package installation

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type bootstrapFixture struct {
	script, root, home, bin string
	env                     map[string]string
}

func newBootstrapFixture(t *testing.T) *bootstrapFixture {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the bootstrap requires Linux")
	}
	script, err := filepath.Abs("../../web/public/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	f := &bootstrapFixture{script: script, root: t.TempDir(), env: map[string]string{}}
	f.home = filepath.Join(f.root, "home with spaces")
	f.bin = filepath.Join(f.root, "bin")
	for _, dir := range []string{f.home, f.bin, filepath.Join(f.root, "storage")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"uname", "id", "mkdir", "chmod", "stat", "df", "awk", "cat", "env", "mktemp", "rm", "rmdir"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Symlink(path, filepath.Join(f.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	f.env = map[string]string{"HOME": f.home, "PATH": f.bin, "DOCKER_HOST": "", "DOCKER_CONTEXT": "", "DOODSHIP_IMAGE": "", "TEST_ROOT": f.root, "TEST_ENDPOINT": "unix:///var/run/docker.sock", "TEST_SERVER": "linux/x86_64", "TEST_STORAGE": filepath.Join(f.root, "storage"), "TEST_IMAGE_ID": "sha256:" + strings.Repeat("a", 64), "TEST_COMPOSE": "2.40.1"}
	f.stub(t, "uname", "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; *) echo x86_64 ;; esac\n")
	f.stub(t, "docker", `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$TEST_ROOT/trace"
if [ "$1" = --host ]; then shift 2; fi
case "$1" in
 context) [ "${TEST_CONTEXT_FAILURE:-0}" = 0 ] || exit 1; printf '%s\n' "$TEST_ENDPOINT" ;;
 info)
  [ "${TEST_DOCKER_FAILURE:-0}" = 0 ] || exit 1
  case "$*" in *DockerRootDir*) printf '%s\n' "$TEST_STORAGE" ;; *) printf '%s\n' "$TEST_SERVER" ;; esac ;;
 pull) exit "${TEST_PULL_FAILURE:-0}" ;;
 image) [ "${TEST_IMAGE_MISSING:-0}" = 0 ] || exit 1
  case "$*" in *Architecture*) printf '%s\n' "${TEST_IMAGE_PLATFORM:-linux/amd64}" ;; *) printf '%s\n' "$TEST_IMAGE_ID" ;; esac ;;
 run)
  case " $* " in
   *" --entrypoint docker "*)
    case "$*" in *"compose version"*) printf '%s\n' "$TEST_COMPOSE" ;; *) exit "${TEST_PROBE_FAILURE:-0}" ;; esac ;;
   *" --entrypoint /usr/local/bin/dispatch "*) exit "${TEST_SETUP_MISSING:-0}" ;;
   *" --entrypoint sh "*)
    for value do
     case "$value" in type=bind,src=*,dst=/host-check)
      directory=${value#type=bind,src=}; directory=${directory%,dst=/host-check}
      printf '#!/bin/sh\nexit %s\n' "${TEST_NATIVE_FAILURE:-0}" > "$directory/dispatch"
      chmod 755 "$directory/dispatch" ;;
     esac
    done ;;
   *) printf '%s\n' "$@" > "$TEST_ROOT/args"; env > "$TEST_ROOT/environment"; cat > "$TEST_ROOT/input" ;;
  esac ;;
 *) exit 1 ;;
esac
`)
	f.stub(t, "df", `#!/bin/sh
set -eu
printf 'Filesystem 1024-blocks Used Available Capacity Mounted\n'
if [ "$2" = "$HOME" ]; then available=${TEST_HOME_AVAILABLE:-8388608}; else available=${TEST_DOCKER_AVAILABLE:-8388608}; fi
printf 'fixture 99999999 0 %s 0%% /\n' "$available"
`)
	return f
}
func (f *bootstrapFixture) stub(t *testing.T, name, script string) {
	t.Helper()
	path := filepath.Join(f.bin, name)
	// Replace only our fixture symlinks, never their system command targets.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}
func (f *bootstrapFixture) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command := exec.Command("sh", append([]string{f.script}, args...)...)
	command.Env = []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, override := f.env[key]; !override {
			command.Env = append(command.Env, entry)
		}
	}
	for key, value := range f.env {
		command.Env = append(command.Env, key+"="+value)
	}
	command.Stdin = strings.NewReader(`{"enrollmentToken":"secret-stays-on-stdin","remoteAccess":true}`)
	output, err := command.CombinedOutput()
	return string(output), err
}
func (f *bootstrapFixture) read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCurlBootstrapPreservesInputAndPinsFreshImage(t *testing.T) {
	for _, pull := range []bool{true, false} {
		t.Run(map[bool]string{true: "pull", false: "local"}[pull], func(t *testing.T) {
			f := newBootstrapFixture(t)
			args := []string{"--name", "my-server", "--port=8081"}
			if !pull {
				args = append(args, "--pull=false")
			}
			if output, err := f.run(t, args...); err != nil {
				t.Fatalf("%s: %v", output, err)
			}
			if input := f.read(t, "input"); input != `{"enrollmentToken":"secret-stays-on-stdin","remoteAccess":true}` {
				t.Fatal("connection input changed")
			}
			for _, file := range []string{"args", "environment", "trace"} {
				if strings.Contains(f.read(t, file), "secret-stays-on-stdin") {
					t.Fatal("token leaked into", file)
				}
			}
			invocation := f.read(t, "args")
			for _, expected := range []string{"--connection-stdin", "--pull=never", "--port=8081", "type=bind,src=" + f.home + "/.doodship,dst=" + f.home + "/.doodship", f.env["TEST_IMAGE_ID"], "--host-uid", "--host-gid"} {
				if !strings.Contains(invocation, expected) {
					t.Fatal("missing argument", expected)
				}
			}
			if !strings.Contains(invocation, "--name\ndoodship\n--name\nmy-server\n") {
				t.Fatal("custom project name was overridden")
			}
			if strings.Contains(f.read(t, "trace"), " pull ") != pull {
				t.Fatal("incorrect image pull policy")
			}
			info, err := os.Stat(filepath.Join(f.home, ".doodship"))
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatal("installation directory is not private")
			}
		})
	}
}

func TestCurlBootstrapRejectsUnsupportedHostsBeforeInstalling(t *testing.T) {
	cases := []struct {
		name, message string
		env           map[string]string
		args          []string
		prepare       func(*testing.T, *bootstrapFixture)
	}{
		{name: "no-docker", message: "Missing docker", prepare: func(t *testing.T, f *bootstrapFixture) {
			if err := os.Remove(filepath.Join(f.bin, "docker")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "not-linux", message: "Linux server", prepare: func(t *testing.T, f *bootstrapFixture) { f.stub(t, "uname", "#!/bin/sh\nprintf 'Darwin\\n'\n") }},
		{name: "unknown-architecture", message: "amd64 and arm64", prepare: func(t *testing.T, f *bootstrapFixture) {
			f.stub(t, "uname", "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; *) echo riscv64 ;; esac\n")
		}},
		{name: "remote-context", message: "local Docker engine", env: map[string]string{"TEST_ENDPOINT": "ssh://server", "DOCKER_CONTEXT": "remote", "DOCKER_HOST": "unix:///var/run/docker.sock"}},
		{name: "remote-host", message: "local Docker engine", env: map[string]string{"DOCKER_HOST": "tcp://server:2376"}},
		{name: "context-failure", message: "Cannot read", env: map[string]string{"TEST_CONTEXT_FAILURE": "1"}},
		{name: "daemon-unavailable", message: "cannot access", env: map[string]string{"TEST_DOCKER_FAILURE": "1"}},
		{name: "windows-engine", message: "engine must run Linux", env: map[string]string{"TEST_SERVER": "windows/amd64"}},
		{name: "different-engine-architecture", message: "does not match", env: map[string]string{"TEST_SERVER": "linux/arm64"}},
		{name: "empty-home", message: "HOME must point", env: map[string]string{"HOME": ""}},
		{name: "relative-home", message: "absolute path", env: map[string]string{"HOME": "relative"}},
		{name: "unsupported-home", message: "characters", prepare: func(t *testing.T, f *bootstrapFixture) {
			directory := filepath.Join(f.root, "home,with,commas")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			f.env["HOME"] = directory
		}},
		{name: "missing-home", message: "must exist", env: map[string]string{"HOME": "/missing/fixture-home"}},
		{name: "symlink", message: "symbolic link", prepare: func(t *testing.T, f *bootstrapFixture) {
			if err := os.Symlink(f.bin, filepath.Join(f.home, ".doodship")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "file", message: "used by a file", prepare: func(t *testing.T, f *bootstrapFixture) {
			if err := os.WriteFile(filepath.Join(f.home, ".doodship"), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "different-owner", message: "user who owns", prepare: func(t *testing.T, f *bootstrapFixture) {
			os.Mkdir(filepath.Join(f.home, ".doodship"), 0700)
			f.stub(t, "stat", "#!/bin/sh\nprintf '999999\\n'\n")
		}},
		{name: "missing-storage", message: "storage must be on this server", env: map[string]string{"TEST_STORAGE": "/missing/storage-fixture"}},
		{name: "low-docker-space", message: "2 GiB", env: map[string]string{"TEST_DOCKER_AVAILABLE": "1024"}},
		{name: "low-home-space", message: "512 MiB", env: map[string]string{"TEST_HOME_AVAILABLE": "1024"}},
		{name: "bad-space-report", message: "Cannot check", env: map[string]string{"TEST_DOCKER_AVAILABLE": "unknown"}},
		{name: "invalid-image", message: "valid image reference", env: map[string]string{"DOODSHIP_IMAGE": "--privileged"}},
		{name: "image-pull-failed", message: "Cannot download", env: map[string]string{"TEST_PULL_FAILURE": "1"}},
		{name: "missing-local-image", message: "not available locally", env: map[string]string{"TEST_IMAGE_MISSING": "1"}, args: []string{"--pull=false"}},
		{name: "engine-incompatible-with-image", message: "cannot use this Docker engine", env: map[string]string{"TEST_PROBE_FAILURE": "1"}},
		{name: "old-compose", message: "Compose 2.30", env: map[string]string{"TEST_COMPOSE": "2.29.0"}},
		{name: "wrong-image-architecture", message: "image does not match", env: map[string]string{"TEST_IMAGE_PLATFORM": "linux/arm64"}},
		{name: "pinned-image-with-updates", message: "requires --auto-update=false", env: map[string]string{"DOODSHIP_IMAGE": "ghcr.io/doout/dispatch@sha256:" + strings.Repeat("a", 64)}},
		{name: "missing-setup-command", message: "does not support", env: map[string]string{"TEST_SETUP_MISSING": "1"}},
		{name: "native-cli-libraries", message: "cannot run the CLI", env: map[string]string{"TEST_NATIVE_FAILURE": "1"}},
		{name: "directory-override", message: "Unsupported setup option", args: []string{"--dir", "secret-stays-on-stdin"}},
		{name: "socket-override", message: "Unsupported setup option", args: []string{"--docker-socket=/other.sock"}},
		{name: "missing-option-value", message: "missing its value", args: []string{"--port"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBootstrapFixture(t)
			for k, v := range tc.env {
				f.env[k] = v
			}
			if tc.prepare != nil {
				tc.prepare(t, f)
			}
			output, err := f.run(t, tc.args...)
			if err == nil || !strings.Contains(output, tc.message) {
				t.Fatalf("unexpected failure: %s %v", output, err)
			}
			leftovers, _ := filepath.Glob(filepath.Join(f.home, ".doodship-check.*"))
			if len(leftovers) != 0 {
				t.Fatal("CLI compatibility probe left files behind")
			}

			if strings.Contains(output, "secret-stays-on-stdin") {
				t.Fatal("error leaked a token")
			}
			if _, err := os.Stat(filepath.Join(f.root, "args")); !os.IsNotExist(err) {
				t.Fatal("failed prerequisite ran setup")
			}
			if _, err := os.Stat(filepath.Join(f.home, ".doodship", "local-owner.json")); err == nil {
				t.Fatal("failed prerequisite created an owner")
			}
		})
	}
}

func TestCurlBootstrapResolvesHomeSymlinks(t *testing.T) {
	f := newBootstrapFixture(t)
	alias := filepath.Join(f.root, "home-link")
	if err := os.Symlink(f.home, alias); err != nil {
		t.Fatal(err)
	}
	f.env["HOME"] = alias
	if output, err := f.run(t); err != nil {
		t.Fatalf("%s: %v", output, err)
	}
	args := f.read(t, "args")
	if strings.Contains(args, alias) || !strings.Contains(args, f.home+"/.doodship") {
		t.Fatal("installation paths differ from the host CLI's resolved directory")
	}
}

func TestCurlBootstrapSupportsARM64AndHelpWithoutDocker(t *testing.T) {
	t.Run("arm64", func(t *testing.T) {
		f := newBootstrapFixture(t)
		f.stub(t, "uname", "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; *) echo aarch64 ;; esac\n")
		f.env["TEST_SERVER"] = "linux/aarch64"
		f.env["TEST_IMAGE_PLATFORM"] = "linux/arm64"
		if output, err := f.run(t, "--pull=false"); err != nil {
			t.Fatalf("%s: %v", output, err)
		}
	})
	t.Run("help", func(t *testing.T) {
		f := newBootstrapFixture(t)
		os.Remove(filepath.Join(f.bin, "docker"))
		if output, err := f.run(t, "--help"); err != nil || !strings.Contains(output, "Usage:") {
			t.Fatalf("%s: %v", output, err)
		}
		if _, err := os.Stat(filepath.Join(f.home, ".doodship")); !os.IsNotExist(err) {
			t.Fatal("help created installation files")
		}
	})
}
