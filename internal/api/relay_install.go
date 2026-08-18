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
command -v systemctl >/dev/null 2>&1 || { echo "systemd is required" >&2; exit 1; }
command -v useradd >/dev/null 2>&1 || { echo "useradd is required" >&2; exit 1; }

install_root=/usr/local/bin
state_root=/var/lib/dispatch-relay
config_root=/etc/dispatch-relay
binary_path=${DISPATCH_RELAY_BINARY_PATH:-}

if [ -z "$binary_path" ]; then
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
fi

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
	PrivateKeyPassword string `json:"privateKeyPassword"`
	SudoPassword       string `json:"sudoPassword"`
	HostKeyFingerprint string `json:"hostKeyFingerprint"`
	RelayURL           string `json:"relayUrl"`
	RelayToken         string `json:"relayToken"`
}

var relayInstallTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{24,256}$`)

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
	auth, err := relaySSHAuth(input)
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

	sudo := "sudo -n"
	stdin := relayInstaller
	if input.User == "root" {
		sudo = ""
	} else if input.SudoPassword != "" {
		sudo = "sudo -S -p ''"
		stdin = input.SudoPassword + "\n" + stdin
	}
	command := strings.TrimSpace(sudo + " env " +
		"DISPATCH_RELAY_PUBLIC_URL=" + shellQuote(relayURL) + " " +
		"DISPATCH_RELAY_TOKEN=" + shellQuote(input.RelayToken) + " " +
		"DISPATCH_RELAY_BINARY_PATH=" + shellQuote(temporaryPath) + " sh -s")
	output, err := runSSHCommand(client, command, strings.NewReader(stdin))
	if err != nil {
		problem(w, http.StatusBadGateway, "Relay installation failed", strings.TrimSpace(string(output))+"\n"+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "installed", "output": strings.TrimSpace(string(output))})
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

func relaySSHAuth(input relaySSHInstallRequest) (ssh.AuthMethod, error) {
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
