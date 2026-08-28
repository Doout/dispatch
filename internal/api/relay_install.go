package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/ssh"
)

const relayInstaller = `#!/bin/sh
set -eu

: "${DISPATCH_RELAY_PUBLIC_URL:?DISPATCH_RELAY_PUBLIC_URL is required}"
: "${DISPATCH_RELAY_TOKEN:?DISPATCH_RELAY_TOKEN is required}"

case "$DISPATCH_RELAY_PUBLIC_URL" in https://*) ;; *) echo "Relay URL must use https" >&2; exit 1 ;; esac
case "$DISPATCH_RELAY_TOKEN" in *[!A-Za-z0-9_-]*) echo "Relay token contains unsupported characters" >&2; exit 1 ;; esac
if [ "${#DISPATCH_RELAY_TOKEN}" -lt 24 ] || [ "${#DISPATCH_RELAY_TOKEN}" -gt 256 ]; then
  echo "Relay token must contain 24-256 characters" >&2
  exit 1
fi

install_root=/usr/local/bin
state_root=/var/lib/dispatch-relay
config_root=/etc/dispatch-relay
binary_path=${DISPATCH_RELAY_BINARY_PATH:-}
install_mode=${DISPATCH_RELAY_INSTALL_MODE:-systemd}

download_binary() {
  if [ -n "$binary_path" ]; then
    return
  fi
  : "${DISPATCH_RELAY_DOWNLOAD_BASE:?DISPATCH_RELAY_DOWNLOAD_BASE is required}"
  command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 1; }
  machine=$(uname -m)
  case "$machine" in
    x86_64|amd64) machine=amd64 ;;
    aarch64|arm64) machine=arm64 ;;
    *) echo "Unsupported architecture: $machine" >&2; exit 1 ;;
  esac
  binary_path=$(mktemp)
  trap 'rm -f "$binary_path"' EXIT
  curl -fsSL "${DISPATCH_RELAY_DOWNLOAD_BASE%/}/relay/bin/linux-$machine" -o "$binary_path"
}

if [ "$install_mode" = docker ]; then
  command -v docker >/dev/null 2>&1 || { echo "Docker is required" >&2; exit 1; }
  docker compose version >/dev/null 2>&1 || { echo "Docker Compose v2 is required" >&2; exit 1; }

  relay_root=${DISPATCH_RELAY_DOCKER_ROOT:-/opt/dispatch-relay}
  relay_image=${DISPATCH_RELAY_IMAGE:-dispatch-relay:local}
  case "$relay_image" in *[!A-Za-z0-9._/:@-]*) echo "Relay image contains unsupported characters" >&2; exit 1 ;; esac

  install -d -m 0755 "$relay_root" "$config_root"
  install -d -m 0750 "$state_root"
  chown -R 65532:65532 "$state_root"

  if [ -z "${DISPATCH_RELAY_IMAGE:-}" ]; then
    download_binary
    image_root="$relay_root/image"
    install -d -m 0755 "$image_root"
    install -m 0755 "$binary_path" "$image_root/dispatch-relay"
    cat > "$image_root/Containerfile" <<'EOF'
FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY dispatch-relay /usr/local/bin/dispatch-relay
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/dispatch-relay"]
EOF
    docker build --pull -f "$image_root/Containerfile" -t "$relay_image" "$image_root"
  else
    docker pull "$relay_image"
  fi

  cat > "$config_root/relay.env" <<EOF
DISPATCH_RELAY_ADDR=:443
DISPATCH_RELAY_PUBLIC_URL=$DISPATCH_RELAY_PUBLIC_URL
DISPATCH_RELAY_TOKEN=$DISPATCH_RELAY_TOKEN
DISPATCH_RELAY_TLS_MODE=auto
DISPATCH_RELAY_ACME_CACHE=/data/acme
DATABASE_URL=/data/relay.db
EOF
  chmod 0600 "$config_root/relay.env"

  cat > "$relay_root/compose.yaml" <<EOF
services:
  relay:
    image: $relay_image
    restart: unless-stopped
    env_file:
      - $config_root/relay.env
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - $state_root:/data
    cap_add:
      - NET_BIND_SERVICE
    security_opt:
      - no-new-privileges:true
    read_only: true
    tmpfs:
      - /tmp
EOF
  docker compose -f "$relay_root/compose.yaml" up -d --remove-orphans
  docker compose -f "$relay_root/compose.yaml" ps
  echo "Relay installed with Docker Compose. Re-run this command to update it."
  exit 0
fi

if [ "$install_mode" != systemd ]; then
  echo "DISPATCH_RELAY_INSTALL_MODE must be systemd or docker" >&2
  exit 1
fi

command -v systemctl >/dev/null 2>&1 || { echo "systemd is required" >&2; exit 1; }
command -v useradd >/dev/null 2>&1 || { echo "useradd is required" >&2; exit 1; }
download_binary

install -d -m 0755 "$install_root" "$config_root"
install -d -m 0750 "$state_root"
install -m 0755 "$binary_path" "$install_root/dispatch-relay"

cat > "$config_root/relay.env" <<EOF
DISPATCH_RELAY_ADDR=:443
DISPATCH_RELAY_PUBLIC_URL=$DISPATCH_RELAY_PUBLIC_URL
DISPATCH_RELAY_TOKEN=$DISPATCH_RELAY_TOKEN
DISPATCH_RELAY_TLS_MODE=auto
DISPATCH_RELAY_ACME_CACHE=$state_root/acme
DATABASE_URL=$state_root/relay.db
EOF
chmod 0600 "$config_root/relay.env"

cat > /etc/systemd/system/dispatch-relay.service <<'EOF'
[Unit]
Description=Dispatch durable webhook relay
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/dispatch-relay/relay.env
ExecStart=/usr/local/bin/dispatch-relay
Restart=always
RestartSec=3
User=dispatch-relay
Group=dispatch-relay
StateDirectory=dispatch-relay
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/dispatch-relay

[Install]
WantedBy=multi-user.target
EOF

if ! id dispatch-relay >/dev/null 2>&1; then
  useradd --system --home-dir "$state_root" --shell /usr/sbin/nologin dispatch-relay
fi
chown -R dispatch-relay:dispatch-relay "$state_root"
systemctl daemon-reload
systemctl enable --now dispatch-relay
systemctl --no-pager --full status dispatch-relay | sed -n '1,12p'
`

type relaySSHScanRequest struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type relaySSHInstallRequest struct {
	Host               string `json:"host"`
	Port               int    `json:"port"`
	User               string `json:"user"`
	AuthType           string `json:"authType"`
	Password           string `json:"password"`
	PrivateKey         string `json:"privateKey"`
	SecretID           string `json:"secretId"`
	PrivateKeyPassword string `json:"privateKeyPassword"`
	SudoPassword       string `json:"sudoPassword"`
	HostKeyFingerprint string `json:"hostKeyFingerprint"`
	RelayURL           string `json:"relayUrl"`
	RelayToken         string `json:"relayToken"`
	InstallMode        string `json:"installMode"`
	RelayImage         string `json:"relayImage"`
}

var relayInstallTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{24,256}$`)
var relayInstallImagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)

func (a *API) relayInstallScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = io.WriteString(w, relayInstaller)
}

func (a *API) relayBinary(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(strings.TrimSpace(chi.URLParam(r, "platform")))
	if name != "linux-amd64" && name != "linux-arm64" {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(relayBinaryRoot(), name)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="dispatch-relay"`)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, path)
}

func relayBinaryRoot() string {
	if root := strings.TrimSpace(os.Getenv("DISPATCH_RELAY_BINARY_ROOT")); root != "" {
		return root
	}
	return "/usr/local/lib/dispatch-relay"
}

func (a *API) scanRelaySSHHost(w http.ResponseWriter, r *http.Request) {
	var input relaySSHScanRequest
	if !decode(w, r, &input) {
		return
	}
	address, detail := relaySSHAddress(input.Host, input.Port)
	if detail != "" {
		problem(w, http.StatusBadRequest, "SSH address invalid", detail)
		return
	}
	fingerprint, err := scanSSHHostKey(r.Context(), address)
	if err != nil {
		problem(w, http.StatusBadGateway, "SSH host unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"fingerprint": fingerprint})
}

func scanSSHHostKey(ctx context.Context, address string) (string, error) {
	var fingerprint string
	dialer := net.Dialer{Timeout: 8 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	config := &ssh.ClientConfig{
		User: "dispatch-host-scan",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fingerprint = ssh.FingerprintSHA256(key)
			return errors.New("host key captured")
		},
		Timeout: 8 * time.Second,
	}
	_, _, _, _ = ssh.NewClientConn(conn, address, config)
	if fingerprint == "" {
		return "", errors.New("the server did not present an SSH host key")
	}
	return fingerprint, nil
}

func (a *API) installRelayOverSSH(w http.ResponseWriter, r *http.Request) {
	var input relaySSHInstallRequest
	if !decode(w, r, &input) {
		return
	}
	address, detail := relaySSHAddress(input.Host, input.Port)
	if detail != "" {
		problem(w, http.StatusBadRequest, "SSH address invalid", detail)
		return
	}
	input.User = strings.TrimSpace(input.User)
	input.HostKeyFingerprint = strings.TrimSpace(input.HostKeyFingerprint)
	if input.User == "" || input.HostKeyFingerprint == "" {
		problem(w, http.StatusBadRequest, "SSH identity required", "Enter an SSH user and verify the node fingerprint before installing.")
		return
	}
	relayURL, detail := validateRelayAddress(input.RelayURL)
	if detail != "" || !strings.HasPrefix(relayURL, "https://") {
		problem(w, http.StatusBadRequest, "Relay URL invalid", "Enter the public HTTPS URL for this relay.")
		return
	}
	input.RelayToken = strings.TrimSpace(input.RelayToken)
	if !relayInstallTokenPattern.MatchString(input.RelayToken) {
		problem(w, http.StatusBadRequest, "Relay token invalid", "Use 24-256 letters, numbers, underscores, or hyphens.")
		return
	}
	input.InstallMode, input.RelayImage, detail = relayInstallOptions(input.InstallMode, input.RelayImage)
	if detail != "" {
		problem(w, http.StatusBadRequest, "Relay runtime invalid", detail)
		return
	}
	auth, err := a.relaySSHAuth(r.Context(), input)
	if err != nil {
		problem(w, http.StatusBadRequest, "SSH credential invalid", err.Error())
		return
	}
	config := &ssh.ClientConfig{
		User: input.User, Auth: []ssh.AuthMethod{auth}, Timeout: 12 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			actual := ssh.FingerprintSHA256(key)
			if actual != input.HostKeyFingerprint {
				return fmt.Errorf("SSH host key changed: expected %s, received %s", input.HostKeyFingerprint, actual)
			}
			return nil
		},
	}
	client, err := ssh.Dial("tcp", address, config)
	if err != nil {
		problem(w, http.StatusBadGateway, "SSH connection failed", err.Error())
		return
	}
	defer client.Close()

	archOutput, err := runSSHCommand(client, "uname -m", nil)
	if err != nil {
		problem(w, http.StatusBadGateway, "Node inspection failed", err.Error())
		return
	}
	platform, err := relayPlatform(strings.TrimSpace(string(archOutput)))
	if err != nil {
		problem(w, http.StatusBadRequest, "Node architecture unsupported", err.Error())
		return
	}
	binary, err := os.ReadFile(filepath.Join(relayBinaryRoot(), platform))
	if err != nil {
		problem(w, http.StatusServiceUnavailable, "Relay installer unavailable", "This controller image does not contain the relay binary for the selected node.")
		return
	}
	temporaryPath := "/tmp/dispatch-relay-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(binary)))
	base64.StdEncoding.Encode(encoded, binary)
	_, err = runSSHCommand(client, "base64 -d > "+shellQuote(temporaryPath), bytes.NewReader(encoded))
	if err != nil {
		problem(w, http.StatusBadGateway, "Relay transfer failed", err.Error())
		return
	}
	defer func() { _, _ = runSSHCommand(client, "rm -f "+shellQuote(temporaryPath), nil) }()

	command, stdin := relaySSHInstallerInvocation(input, relayURL, temporaryPath)
	output, err := runSSHCommand(client, command, strings.NewReader(stdin))
	if err != nil {
		problem(w, http.StatusBadGateway, "Relay installation failed", strings.TrimSpace(string(output))+"\n"+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "installed", "output": strings.TrimSpace(string(output))})
}

func relayInstallOptions(mode, image string) (string, string, string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	image = strings.TrimSpace(image)
	if mode == "" {
		mode = "systemd"
	}
	if mode != "systemd" && mode != "docker" {
		return "", "", "Choose systemd or Docker Compose."
	}
	if image != "" && mode != "docker" {
		return "", "", "A container image can only be used with Docker Compose."
	}
	if len(image) > 512 || image != "" && !relayInstallImagePattern.MatchString(image) {
		return "", "", "Enter a valid container image reference."
	}
	return mode, image, ""
}

func relaySSHInstallerInvocation(input relaySSHInstallRequest, relayURL, temporaryPath string) (string, string) {
	sudo := "sudo -n"
	stdin := relayInstaller
	if input.User == "root" {
		sudo = ""
	} else if input.SudoPassword != "" {
		sudo = "sudo -S -p ''"
		stdin = input.SudoPassword + "\n" + stdin
	}
	environment := []string{
		"DISPATCH_RELAY_PUBLIC_URL=" + shellQuote(relayURL),
		"DISPATCH_RELAY_TOKEN=" + shellQuote(input.RelayToken),
		"DISPATCH_RELAY_BINARY_PATH=" + shellQuote(temporaryPath),
	}
	if input.InstallMode == "docker" {
		environment = append(environment, "DISPATCH_RELAY_INSTALL_MODE='docker'")
		if input.RelayImage != "" {
			environment = append(environment, "DISPATCH_RELAY_IMAGE="+shellQuote(input.RelayImage))
		}
	}
	return strings.TrimSpace(sudo + " env " + strings.Join(environment, " ") + " sh -s"), stdin
}

func relaySSHAddress(host string, port int) (string, string) {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " /?#@") {
		return "", "Enter a hostname or IP address."
	}
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return "", "Use an SSH port between 1 and 65535."
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port)), ""
}

func (a *API) relaySSHAuth(ctx context.Context, input relaySSHInstallRequest) (ssh.AuthMethod, error) {
	input.SecretID = strings.TrimSpace(input.SecretID)
	if input.SecretID != "" {
		if input.Password != "" || input.PrivateKey != "" {
			return nil, errors.New("choose a saved SSH key or enter a credential")
		}
		if a.secretResolver == nil {
			return nil, errors.New("encrypted secret storage is not configured")
		}
		secret, err := a.store.GetSecret(ctx, input.SecretID)
		if err != nil {
			return nil, errors.New("saved SSH key not found")
		}
		if secret.Type != core.SecretTypeSSHPrivateKey {
			return nil, errors.New("choose a saved SSH private key")
		}
		plaintext, err := a.secretResolver.Resolve(ctx, secret.ID)
		if err != nil {
			return nil, errors.New("saved SSH key could not be resolved")
		}
		defer func() {
			for index := range plaintext {
				plaintext[index] = 0
			}
		}()
		input.AuthType = "private_key"
		input.PrivateKey = string(plaintext)
	}
	return relaySSHAuthInput(input)
}

func relaySSHAuthInput(input relaySSHInstallRequest) (ssh.AuthMethod, error) {
	switch strings.ToLower(strings.TrimSpace(input.AuthType)) {
	case "password":
		if input.Password == "" {
			return nil, errors.New("enter the SSH password")
		}
		return ssh.Password(input.Password), nil
	case "private_key":
		var signer ssh.Signer
		var err error
		if input.PrivateKeyPassword != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(input.PrivateKey), []byte(input.PrivateKeyPassword))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(input.PrivateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		return ssh.PublicKeys(signer), nil
	default:
		return nil, errors.New("choose password or private key authentication")
	}
}

func relayPlatform(machine string) (string, error) {
	switch strings.ToLower(machine) {
	case "x86_64", "amd64":
		return "linux-amd64", nil
	case "aarch64", "arm64":
		return "linux-arm64", nil
	default:
		return "", fmt.Errorf("Linux %s is not supported", machine)
	}
}

func runSSHCommand(client *ssh.Client, command string, stdin io.Reader) ([]byte, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	if stdin != nil {
		session.Stdin = stdin
	}
	return session.CombinedOutput(command)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
