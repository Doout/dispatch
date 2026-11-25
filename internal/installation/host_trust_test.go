package installation

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHostCABundleProbeAcceptsOnlyPublicCertificates(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	server.Close()
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	for _, tc := range []struct {
		name, contents string
		valid          bool
	}{
		{"distro bundle", "# root certificates\n" + cert + "\n" + cert, true},
		{"malformed certificate", "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n", false},
		{"bundle with private key", cert + "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.crt")
			if err := os.WriteFile(path, []byte(tc.contents), 0644); err != nil {
				t.Fatal(err)
			}
			m := New(t.TempDir())
			m.Run = func(ctx context.Context, name string, args ...string) (string, error) {
				if !strings.Contains(strings.Join(args, " "), "--network none") || !strings.Contains(strings.Join(args, " "), ",readonly") {
					t.Fatalf("unclear probe isolation: %v", args)
				}
				return Command(ctx, "sh", "-c", strings.ReplaceAll(args[len(args)-1], "/tmp/dispatch-host-ca", path))
			}
			bundle, _, err := findHostCABundle(context.Background(), m.Run, "test:image")
			if err != nil || (bundle != "") != tc.valid {
				t.Fatalf("bundle %q, err %v", bundle, err)
			}
		})
	}
}

func TestExistingContainerExportLeavesHostTrustForDestination(t *testing.T) {
	c := existingContainerFixture()
	c.Mounts = append(c.Mounts, containerMount{Type: "bind", Source: controllerCABundle, Destination: controllerCABundle})
	config, _, extra, _, err := exportContainerConfig(c)
	if err != nil || config.HostCABundle != "" || len(extra) != 1 || extra[0].Destination != "/kubeconfigs" {
		t.Fatal("export included source-host trust", err)
	}
}

func TestOldManagedControllerUsesHostCAWithoutInstallerChanges(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	server.Close()
	bundle := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	labels := strings.Repeat("b", 32)
	selfID := strings.Repeat("a", 64)
	image := "sha256:" + strings.Repeat("c", 64)
	for _, tc := range []struct {
		name, service, volumeLabel     string
		customCA, mounted, expectTrust bool
	}{
		{name: "legacy managed", service: "dispatch", volumeLabel: labels, expectTrust: true},
		{name: "explicit settings", service: "dispatch", volumeLabel: labels, customCA: true},
		{name: "already mounted", service: "dispatch", volumeLabel: labels, mounted: true},
		{name: "unmanaged data volume", service: "dispatch", volumeLabel: "different"},
		{name: "hosted platform", service: "platform", volumeLabel: labels},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range caEnvironment {
				t.Setenv(key, "")
				os.Unsetenv(key)
			}
			t.Setenv("SSL_CERT_DIR", "")
			if tc.customCA {
				t.Setenv("GIT_SSL_CAINFO", "/operator-ca.pem")
			}
			mounts := []containerMount{{Type: "volume", Name: "doodship-data", Destination: "/data", RW: true}, {Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"}}
			if tc.mounted {
				mounts = append(mounts, containerMount{Type: "bind", Source: controllerCABundle, Destination: controllerCABundle})
			}
			mountJSON, err := json.Marshal(mounts)
			if err != nil {
				t.Fatal(err)
			}
			var probes int
			run := func(ctx context.Context, command string, args ...string) (string, error) {
				switch args[0] {
				case "inspect":
					return strings.Join([]string{selfID, image, labels, "doodship", tc.service, string(mountJSON)}, "\n"), nil
				case "volume":
					return tc.volumeLabel, nil
				case "run":
					probes++
					if !slices.Contains(args, image) {
						t.Fatal("probe did not use the running controller image")
					}
					return bundle, nil
				default:
					t.Fatalf("unexpected command %s %v", command, args)
					return "", nil
				}
			}
			cleanup, err := setupControllerHostTrust(context.Background(), selfID[:12], run)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			if (probes == 1) != tc.expectTrust {
				t.Fatalf("probes %d", probes)
			}
			if !tc.expectTrust {
				if tc.customCA && os.Getenv("GIT_SSL_CAINFO") != "/operator-ca.pem" {
					t.Fatal("custom trust was changed")
				}
				return
			}
			path := os.Getenv("SSL_CERT_FILE")
			got, err := os.ReadFile(path)
			if err != nil || string(got) != bundle || os.Getenv("GIT_SSL_CAINFO") != path || os.Getenv("GIT_PROXY_SSL_CAINFO") != path {
				t.Fatal("CA file or trust environment did not match", err)
			}
			st, err := os.Stat(path)
			if err != nil || st.Mode().Perm() != 0600 {
				t.Fatal("CA file permissions", err)
			}
			cleanup()
			if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("temporary CA file was retained")
			}
		})
	}
}
