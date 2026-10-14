package installation

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/instanceconnection"
)

func TestConnectionInputRejectsUnsafeAndMalformedJSONWithoutLeakingToken(t *testing.T) {
	valid := `{"controlPlaneURL":"https://doodship.test","instanceID":"instance","enrollmentToken":"private-once","remoteAccess":true}`
	for _, raw := range []string{valid + valid, valid + "junk", strings.Replace(valid, "https://doodship.test", "http://doodship.test", 1), strings.Replace(valid, "https://doodship.test", "https://user:pass@doodship.test", 1), strings.Replace(valid, "https://doodship.test", "https://doodship.test/path", 1), strings.Replace(valid, "\"remoteAccess\":true", "\"remoteAccess\":false", 1), strings.Replace(valid, "private-once", strings.Repeat("a", 1025), 1), strings.Replace(valid, "\"instanceID\":\"instance\",", "", 1), strings.Replace(valid, "remoteAccess", "unexpected", 1)} {
		_, err := ReadConnection(strings.NewReader(raw))
		if err == nil || strings.Contains(err.Error(), "private-once") {
			t.Fatalf("unexpected validation error: %v", err)
		}
	}
	if _, err := ReadConnection(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
}

func TestSetupResumesEnrollmentWithoutReplacingOrConsumingAnotherToken(t *testing.T) {
	m, c, _ := fixture(t)
	configured, setups, enrollments := false, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/status" && r.URL.Path != "/api/v1/auth/setup" {
			user, password, ok := r.BasicAuth()
			if !ok || user != "owner" || password != "private-password" {
				w.WriteHeader(401)
				return
			}
		}
		switch r.URL.Path {
		case "/api/v1/auth/status":
			json.NewEncoder(w).Encode(map[string]bool{"setupRequired": setups == 0})
		case "/api/v1/auth/setup":
			setups++
			w.WriteHeader(201)
		case "/api/v1/auth/me":
			w.Write([]byte(`{"identity":{"id":"local-owner","systemRole":"owner"}}`))
		case "/api/v1/instance-connection":
			if r.Method == "PUT" {
				var input instanceconnection.Setup
				json.NewDecoder(r.Body).Decode(&input)
				if input.OwnerUserID != "local-owner" || input.ExpectedInstanceID != "instance" || !input.RemoteAccess {
					t.Error("missing owner or instance approval")
				}
				enrollments++
				if enrollments == 1 {
					http.Error(w, "private-once", 502)
					return
				}
				configured = true
			}
			json.NewEncoder(w).Encode(instanceconnection.Status{Configured: configured, InstanceID: "instance", ControlPlaneURL: "https://doodship.test"})
		default:
			t.Error("unexpected setup request")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	c.Port, _ = strconv.Atoi(port)
	saveJSON(m.path("installation.json"), c)
	m.writeCompose(c)
	saveJSON(m.path("local-owner.json"), setupOwner{UID: os.Getuid(), GID: os.Getgid(), Username: "owner", Password: "private-password", Name: c.Name, Volume: c.Volume, InstallationID: c.ID})
	original := m.Run
	m.Run = func(ctx context.Context, command string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "cp" {
			return "", os.WriteFile(args[len(args)-1], []byte("test-cli"), 0600)
		}
		return original(ctx, command, args...)
	}
	input := ConnectionInput{ControlPlaneURL: "https://doodship.test", InstanceID: "instance", EnrollmentToken: "private-once", RemoteAccess: true}
	if err := m.Setup(context.Background(), c, input, false, false, os.Getuid(), os.Getgid()); err == nil || strings.Contains(err.Error(), input.EnrollmentToken) {
		t.Fatalf("bad failure: %v", err)
	}
	if err := m.Setup(context.Background(), c, input, false, false, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if err := m.Setup(context.Background(), c, input, false, false, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if setups != 1 || enrollments != 2 {
		t.Fatalf("setup duplicated or enrollment repeated: %d %d", setups, enrollments)
	}
	input.InstanceID = "other"
	if err := m.Setup(context.Background(), c, input, false, false, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("accepted another instance")
	}
}

func TestSetupDoesNotAdoptExistingInstallationOrExposePort(t *testing.T) {
	m, c, calls := fixture(t)
	input := ConnectionInput{ControlPlaneURL: "https://doodship.test", InstanceID: "instance", EnrollmentToken: "private-once", RemoteAccess: true}
	if err := m.Setup(context.Background(), c, input, false, false, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("adopted existing installation")
	}
	c.Bind = "0.0.0.0"
	if err := m.Setup(context.Background(), c, input, false, false, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("exposed owner setup publicly")
	}
	if len(*calls) != 0 {
		t.Fatal("changed Docker before validating")
	}
}

func TestSetupPortAndConfigFailuresDoNotCreateOwner(t *testing.T) {
	for _, scenario := range []string{"port", "config"} {
		t.Run(scenario, func(t *testing.T) {
			m, c, calls := fixture(t)
			for _, file := range []string{"installation.json", "compose.json", "dispatch.env"} {
				if err := os.Remove(m.path(file)); err != nil {
					t.Fatal(err)
				}
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			c.Port = listener.Addr().(*net.TCPAddr).Port
			if scenario == "config" {
				c.Name = "invalid project"
			}
			input := ConnectionInput{ControlPlaneURL: "https://doodship.test", InstanceID: "instance", EnrollmentToken: "private-once", RemoteAccess: true}
			if err := m.Setup(context.Background(), c, input, false, false, os.Getuid(), os.Getgid()); err == nil {
				t.Fatal("invalid setup succeeded")
			}
			if _, err := os.Stat(m.path("local-owner.json")); !os.IsNotExist(err) {
				t.Fatal("preflight failure created an owner")
			}
			if len(*calls) != 0 {
				t.Fatal("preflight failure mutated Docker")
			}
		})
	}
}

func TestSetupRecoveryPreservesExistingResources(t *testing.T) {
	for _, scenario := range []string{"unfinished", "missing-controller", "missing-data", "foreign-project", "changed-compose", "changed-image", "pending-upgrade"} {
		t.Run(scenario, func(t *testing.T) {
			m, c, calls := fixture(t)
			owner := setupOwner{UID: os.Getuid(), GID: os.Getgid(), Name: c.Name, Volume: c.Volume, InstallationID: c.ID}
			if scenario == "unfinished" {
				owner.InstallationID = ""
			}
			if scenario == "changed-compose" {
				os.WriteFile(m.path("compose.json"), []byte("changed"), 0600)
			}
			if scenario == "pending-upgrade" {
				os.WriteFile(m.path("upgrade.json"), []byte("{}"), 0600)
			}
			missing := scenario == "unfinished" || scenario == "missing-controller"
			original := m.Run
			m.Run = func(ctx context.Context, command string, args ...string) (string, error) {
				line := strings.Join(args, " ")
				if strings.Contains(line, " ps --all --quiet") && missing {
					return "", nil
				}
				if strings.Contains(line, "up --detach") {
					missing = false
				}
				if strings.HasPrefix(line, "volume inspect") && scenario == "missing-data" {
					return "", errors.New("volume not found")
				}
				if strings.HasPrefix(line, "ps --all --filter label=") && scenario == "foreign-project" {
					return "foreign", nil
				}
				if strings.Contains(line, ".Config.Labels") && strings.HasSuffix(line, " foreign") {
					return "other-installation", nil
				}
				if strings.Contains(line, "{{.Image}}") && scenario == "changed-image" {
					return "sha256:other", nil
				}
				return original(ctx, command, args...)
			}
			err := m.resumeSetup(context.Background(), c, owner)
			shouldRepair := scenario == "unfinished" || scenario == "missing-controller"
			if (err == nil) != shouldRepair {
				t.Fatalf("unexpected recovery result: %v", err)
			}
			if has(*calls, "up --detach") != shouldRepair {
				t.Fatal("recreated an unrelated or changed installation")
			}
			if scenario == "missing-data" && has(*calls, "volume create") {
				t.Fatal("replaced a completed installation's missing database with an empty volume")
			}
		})
	}
}

func TestSyncCLIPreservesBinaryOnFailureAndRejectsSymlinkDirectory(t *testing.T) {
	for _, scenario := range []string{"success", "copy-failed", "empty", "symlink-directory"} {
		t.Run(scenario, func(t *testing.T) {
			m, c, _ := fixture(t)
			if err := os.Mkdir(m.path("bin"), 0700); err != nil {
				t.Fatal(err)
			}
			binary := m.path("bin/dispatch")
			os.WriteFile(binary, []byte("previous-cli"), 0755)
			if scenario == "symlink-directory" {
				os.Rename(m.path("bin"), m.path("actual-bin"))
				os.Symlink(m.path("actual-bin"), m.path("bin"))
			}
			original := m.Run
			m.Run = func(ctx context.Context, command string, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "cp" {
					if scenario == "copy-failed" {
						return "", errors.New("copy interrupted")
					}
					payload := []byte("new-cli")
					if scenario == "empty" {
						payload = nil
					}
					return "", os.WriteFile(args[len(args)-1], payload, 0600)
				}
				return original(ctx, command, args...)
			}
			err := m.syncCLI(context.Background(), c)
			if (err == nil) != (scenario == "success") {
				t.Fatalf("unexpected CLI copy result: %v", err)
			}
			data, _ := os.ReadFile(binary)
			expected := "previous-cli"
			if scenario == "success" {
				expected = "new-cli"
			}
			if string(data) != expected {
				t.Fatal("failed copy damaged the recovery CLI")
			}
			if scenario == "success" {
				info, _ := os.Stat(binary)
				if info.Mode().Perm() != 0755 {
					t.Fatal("new CLI is not executable")
				}
			}
			pending, _ := filepath.Glob(m.path(".cli-*"))
			if len(pending) != 0 {
				t.Fatal("temporary CLI copy was not removed")
			}
		})
	}
}

func TestSetupProjectConflictsDoNotCreateOwner(t *testing.T) {
	m, c, _ := fixture(t)
	for _, file := range []string{"installation.json", "compose.json", "dispatch.env"} {
		os.Remove(m.path(file))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c.Port = listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	original := m.Run
	m.Run = func(ctx context.Context, command string, args ...string) (string, error) {
		if strings.HasPrefix(strings.Join(args, " "), "ps -a") {
			return "foreign-container", nil
		}
		return original(ctx, command, args...)
	}
	input := ConnectionInput{ControlPlaneURL: "https://doodship.test", InstanceID: "instance", EnrollmentToken: "private-once", RemoteAccess: true}
	if err := m.Setup(context.Background(), c, input, false, false, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("adopted another project")
	}
	if _, err := os.Stat(m.path("local-owner.json")); !os.IsNotExist(err) {
		t.Fatal("a project conflict fixed the recovery login to an unusable project name")
	}
}

func TestPreflightUsesDockerContextPrecedenceBeforeContactingEngine(t *testing.T) {
	for _, local := range []bool{true, false} {
		t.Run(map[bool]string{true: "local-context", false: "remote-context"}[local], func(t *testing.T) {
			m, c, calls := fixture(t)
			t.Setenv("DOCKER_CONTEXT", "selected")
			t.Setenv("DOCKER_HOST", "tcp://unused-host:2376")
			original := m.Run
			m.Run = func(ctx context.Context, command string, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "context" {
					if len(args) < 3 || args[2] != "selected" {
						t.Fatal("did not inspect the selected context")
					}
					if !local {
						return "ssh://remote", nil
					}
				}
				return original(ctx, command, args...)
			}
			err := m.preflight(context.Background(), c.Socket)
			if (err == nil) != local {
				t.Fatalf("wrong engine selected: %v", err)
			}
			if !local && has(*calls, "docker info") {
				t.Fatal("contacted a remote engine before rejecting it")
			}
		})
	}
}
