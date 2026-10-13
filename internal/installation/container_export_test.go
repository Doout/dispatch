package installation

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func existingContainerFixture() exportContainer {
	var c exportContainer
	c.ID, c.Image, c.Config.Image = strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64), "dispatch:test"
	c.State.Running = true
	c.Config.Env = []string{"DATABASE_URL=/data/dispatch.db", "DISPATCH_MASTER_KEY_FILE=/data/dispatch-master.key", "DISPATCH_ADMIN_PASSWORD=literal$secret", "PATH=/usr/local/bin:/usr/bin:/bin"}
	c.Mounts = []containerMount{{Type: "volume", Name: "original-data", Source: "/var/lib/docker/volumes/original-data/_data", Destination: "/data", RW: true}, {Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock", RW: true}, {Type: "bind", Source: "/private/kubeconfigs", Destination: "/kubeconfigs", RW: false}}
	c.NetworkSettings.Ports = map[string][]struct{ HostIP, HostPort string }{"8080/tcp": {{HostIP: "127.0.0.1", HostPort: "8080"}}}
	return c
}

func TestExistingContainerRejectsUnsupportedDataBeforeStopping(t *testing.T) {
	for _, change := range []func(*exportContainer){
		func(c *exportContainer) { c.Config.Env[0] = "DATABASE_URL=postgres://private" },
		func(c *exportContainer) { c.Config.Env[1] = "DISPATCH_MASTER_KEY_FILE=/outside/key" },
		func(c *exportContainer) { c.Config.Env = append(c.Config.Env, "SECRET=line\none") },
		func(c *exportContainer) { c.Mounts[2].Destination = "/usr/local/bin" },
		func(c *exportContainer) { c.Mounts = append(c.Mounts, c.Mounts[2]) },
		func(c *exportContainer) { c.Mounts[2].Source = "/private,invalid" },
		func(c *exportContainer) { c.NetworkSettings.Ports = nil },
	} {
		c := existingContainerFixture()
		change(&c)
		if _, _, _, _, err := exportContainerConfig(c); err == nil {
			t.Fatal("unsupported configuration accepted")
		}
	}
	config, data, extra, env, err := exportContainerConfig(existingContainerFixture())
	if err != nil || data.Name != "original-data" || len(extra) != 1 || !config.DataMounts[0].ReadOnly || !bytes.Contains(env, []byte("literal$secret")) {
		t.Fatal("original data, permissions or literal settings lost", err)
	}
	encoded, err := New(t.TempDir()).composeData(config)
	if err != nil || bytes.Contains(encoded, []byte("/private/kubeconfigs")) || !bytes.Contains(encoded, []byte(`"subpath": ".dispatch-export-mounts/0"`)) {
		t.Fatal("restored mount depends on the original host", err)
	}
}

func TestPortableConfigurationMountArchiveRequiresRealDirectories(t *testing.T) {
	for _, kind := range []byte{tar.TypeDir, tar.TypeSymlink, tar.TypeReg} {
		archive := filepath.Join(t.TempDir(), "mounts.tar.gz")
		writeTestTar(t, archive, []*tar.Header{{Name: portableMountRoot + "/0", Typeflag: kind, Mode: 0700, Linkname: "other"}}, [][]byte{nil})
		err := validateArchiveMounts(archive, []DataMount{{Target: "/kubeconfigs", Subpath: portableMountRoot + "/0", ReadOnly: true}})
		if (err == nil) != (kind == tar.TypeDir) {
			t.Fatal("mount directory validation", kind, err)
		}
	}
	for _, mounts := range [][]DataMount{
		{{Target: "/data", Subpath: portableMountRoot + "/0"}},
		{{Target: "/config", Subpath: "../../private"}},
		{{Target: "/config", Subpath: portableMountRoot + "/0"}, {Target: "/config/nested", Subpath: portableMountRoot + "/1"}},
		{{Target: "/config/$HOME", Subpath: portableMountRoot + "/0"}},
	} {
		if err := validateDataMounts(mounts); err == nil {
			t.Fatal("unsafe configuration mount accepted")
		}
	}
}

func TestContainerExportRecoversOriginalControllerAfterCopyFailure(t *testing.T) {
	m := New(t.TempDir())
	m.Out = io.Discard
	m.Health = func(context.Context, Config) error { return nil }
	c := existingContainerFixture()
	encoded, _ := json.Marshal([]exportContainer{c})
	var calls []string
	m.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		switch {
		case strings.HasPrefix(joined, "inspect --type container"):
			return string(encoded), nil
		case strings.HasPrefix(joined, "info"):
			return "29.0.0", nil
		case joined == "compose version --short":
			return "2.30.0", nil
		case strings.HasPrefix(joined, "context inspect"):
			return "unix:///var/run/docker.sock", nil
		case strings.Contains(joined, exportDirectoryLabel):
			return m.Dir, nil
		case strings.HasPrefix(joined, "run --rm"):
			return "", errors.New("archive copy failed")
		}
		return "", nil
	}
	err := m.ExportContainer(context.Background(), "existing", filepath.Join(t.TempDir(), "copy.tar.gz"))
	if err == nil || !has(calls, "stop --time 30 "+c.ID) || !has(calls, "start "+c.ID) {
		t.Fatal("failed export did not restart the original controller", err, calls)
	}
	if _, err = os.Stat(m.path("container-export.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("successful restart retained the recovery journal")
	}
	if has(calls, "compose --project-name") || has(calls, "volume create") {
		t.Fatal("export adopted or recreated the original installation")
	}
}

func TestVerifyExportChecksWithoutDockerAccess(t *testing.T) {
	m := New(t.TempDir())
	var out bytes.Buffer
	m.Out = &out
	m.Run = func(context.Context, string, ...string) (string, error) {
		t.Fatal("verification contacted Docker")
		return "", nil
	}
	if err := m.VerifyExport(context.Background(), exportFixture(t)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Archive verified") || strings.Contains(out.String(), "retained credential") {
		t.Fatal("verification exposed credentials or did not report its result")
	}
	if err := m.VerifyExport(context.Background(), ""); err == nil {
		t.Fatal("missing archive accepted")
	}
}
