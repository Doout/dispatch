package installation

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const controllerCABundle = "/etc/ssl/certs/ca-certificates.crt"

// The management process may itself run in a container. Inspect only the
// distribution trust files on the Docker daemon host that runs the controller.
var hostCABundlePaths = []string{
	controllerCABundle,                 // Debian, Ubuntu, Alpine
	"/etc/pki/tls/certs/ca-bundle.crt", // RHEL and Fedora
	"/etc/ssl/ca-bundle.pem",           // SUSE
	"/etc/ssl/cert.pem",                // Arch
}

var caEnvironment = []string{"SSL_CERT_FILE", "GIT_SSL_CAINFO", "GIT_PROXY_SSL_CAINFO"}

func findHostCABundle(ctx context.Context, run Runner, image string) (string, []byte, error) {
	// --mount never creates a missing path. The helper emits only certificate
	// PEM blocks, after checking the entire file. Root and network are unnecessary.
	const probe = `set -eu
file=/tmp/dispatch-host-ca
[ -f "$file" ] && [ -s "$file" ] && [ -r "$file" ] || exit 0
[ "$(wc -c < "$file")" -le 4194304 ] || exit 0
certs=$(awk '
  { bytes+=length($0)+1; if (bytes>4194304) { invalid=1; exit }; sub(/\r$/, "") }
  !inside && (/^#/ || /^[[:space:]]*$/) { next }
  !inside && /^-----BEGIN CERTIFICATE-----$/ { inside=1; count++; print; next }
  inside && /^-----END CERTIFICATE-----$/ { inside=0; print; next }
  inside && /^[A-Za-z0-9+\/=]+$/ { print; next }
  { invalid=1; exit }
  END { exit invalid || inside || !count }
' "$file") || exit 0
printf '%s\n' "$certs"`
	for _, source := range hostCABundlePaths {
		raw, err := run(ctx, "docker", "run", "--rm", "--pull=never", "--network", "none", "--user", "65532:65532", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--mount", "type=bind,source="+source+",target=/tmp/dispatch-host-ca,readonly", "--entrypoint", "sh", image, "-c", probe)
		if err != nil {
			if strings.Contains(err.Error(), "bind source path does not exist") {
				continue
			}
			// Never include captured helper output in an error.
			return "", nil, fmt.Errorf("could not inspect the host CA bundle at %s", source)
		}
		data := []byte(raw)
		if validPublicCABundle(data) {
			return source, data, nil
		}
	}
	return "", nil, nil
}

func validPublicCABundle(data []byte) bool {
	rest := bytes.TrimSpace(data)
	if len(rest) == 0 || len(data) > 4<<20 {
		return false
	}
	for len(rest) > 0 {
		if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
			return false
		}
		block, next := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return false
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return false
		}
		rest = bytes.TrimSpace(next)
	}
	return true
}

func knownHostCABundle(path string) bool {
	return slices.Contains(hostCABundlePaths, path)
}

// SetupControllerHostTrust supplies legacy CLI-managed controllers with the
// daemon host's trust on first startup after an upgrade. It never rewrites the
// installer files, data volume, or updater. A new installation has a bind mount.
func SetupControllerHostTrust(ctx context.Context, socket string) (func(), error) {
	hostname, err := os.Hostname()
	if err != nil || socket != "/var/run/docker.sock" {
		return func() {}, nil
	}
	run := func(ctx context.Context, name string, args ...string) (string, error) {
		return Command(ctx, name, append([]string{"--host", "unix://" + socket}, args...)...)
	}
	return setupControllerHostTrust(ctx, hostname, run)
}

func setupControllerHostTrust(ctx context.Context, hostname string, run Runner) (func(), error) {
	noop := func() {}
	if (len(hostname) != 12 && len(hostname) != 64) || os.Getenv("SSL_CERT_DIR") != "" {
		return noop, nil
	}
	if _, err := hex.DecodeString(hostname); err != nil {
		return noop, nil
	}
	for _, key := range caEnvironment {
		if _, present := os.LookupEnv(key); present {
			return noop, nil
		}
	}
	// Request only identity information; container environment can hold secrets.
	const inspect = `{{.Id}}
{{.Image}}
{{index .Config.Labels "io.dispatch.installation"}}
{{index .Config.Labels "com.docker.compose.project"}}
{{index .Config.Labels "com.docker.compose.service"}}
{{json .Mounts}}`
	metadata, err := run(ctx, "docker", "inspect", "--type", "container", "--format", inspect, hostname)
	if err != nil {
		// A native process, or a container accessing a different daemon.
		return noop, nil
	}
	fields := strings.SplitN(metadata, "\n", 6)
	if len(fields) != 6 || len(fields[0]) != 64 || !strings.HasPrefix(fields[0], hostname) ||
		!strings.HasPrefix(fields[1], "sha256:") || len(fields[1]) != 71 ||
		len(fields[2]) != 32 || !namePattern.MatchString(fields[3]) || fields[4] != "dispatch" {
		return noop, nil
	}
	for _, id := range []string{fields[0], fields[1][len("sha256:"):], fields[2]} {
		if _, err := hex.DecodeString(id); err != nil {
			return noop, nil
		}
	}
	var mounts []containerMount
	if json.Unmarshal([]byte(fields[5]), &mounts) != nil {
		return noop, nil
	}
	var volume string
	var hasSocket bool
	for _, mount := range mounts {
		switch mount.Destination {
		case controllerCABundle:
			return noop, nil // Both operator mounts and installer mounts take precedence.
		case "/data":
			if mount.Type == "volume" && mount.RW {
				volume = mount.Name
			}
		case "/var/run/docker.sock":
			hasSocket = mount.Type == "bind"
		}
	}
	if !hasSocket || !namePattern.MatchString(volume) {
		return noop, nil
	}
	label, err := run(ctx, "docker", "volume", "inspect", "--format", `{{index .Labels "io.dispatch.installation"}}`, volume)
	if err != nil || label != fields[2] {
		return noop, nil
	}
	_, certs, err := findHostCABundle(ctx, run, fields[1])
	if err != nil || len(certs) == 0 {
		return noop, err
	}
	dir, err := os.MkdirTemp("", "dispatch-host-ca-")
	if err != nil {
		return noop, errors.New("could not create a private directory for controller trust")
	}
	path := filepath.Join(dir, "ca-certificates.crt")
	cleanup := func() {
		for _, key := range caEnvironment {
			if os.Getenv(key) == path {
				os.Unsetenv(key)
			}
		}
		os.RemoveAll(dir)
	}
	if err := os.WriteFile(path, certs, 0600); err != nil {
		cleanup()
		return noop, errors.New("could not write public host certificates for this controller")
	}
	for _, key := range caEnvironment {
		if err := os.Setenv(key, path); err != nil {
			cleanup()
			return noop, err
		}
	}
	return cleanup, nil
}
