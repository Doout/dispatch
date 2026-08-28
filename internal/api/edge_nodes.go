package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

const edgeInstaller = `#!/bin/sh
set -eu

: "${DISPATCH_EDGE_CONTROLLER_URL:?DISPATCH_EDGE_CONTROLLER_URL is required}"
: "${DISPATCH_EDGE_NODE_ID:?DISPATCH_EDGE_NODE_ID is required}"
: "${DISPATCH_EDGE_TOKEN:?DISPATCH_EDGE_TOKEN is required}"
case "$DISPATCH_EDGE_CONTROLLER_URL" in https://*) ;; *) echo "Controller URL must use https" >&2; exit 1 ;; esac
case "$DISPATCH_EDGE_NODE_ID:$DISPATCH_EDGE_TOKEN" in *[!A-Za-z0-9:_-]*) echo "Node credential is invalid" >&2; exit 1 ;; esac

install_mode=${DISPATCH_EDGE_INSTALL_MODE:-docker}
download_base=${DISPATCH_EDGE_DOWNLOAD_BASE:-$DISPATCH_EDGE_CONTROLLER_URL}
machine=$(uname -m)
case "$machine" in
  x86_64|amd64) machine=amd64 ;;
  aarch64|arm64) machine=arm64 ;;
  *) echo "Unsupported architecture: $machine" >&2; exit 1 ;;
esac

temporary=$(mktemp)
trap 'rm -f "$temporary"' EXIT
curl -fsSL "${download_base%/}/edge/bin/linux-$machine" -o "$temporary"
chmod 0755 "$temporary"

config_root=/etc/dispatch-edge
install -d -m 0755 "$config_root"
cat > "$config_root/edge.env" <<EOF
DISPATCH_EDGE_CONTROLLER_URL=$DISPATCH_EDGE_CONTROLLER_URL
DISPATCH_EDGE_NODE_ID=$DISPATCH_EDGE_NODE_ID
DISPATCH_EDGE_TOKEN=$DISPATCH_EDGE_TOKEN
EOF
chmod 0600 "$config_root/edge.env"

if [ "$install_mode" = docker ]; then
  command -v docker >/dev/null 2>&1 || { echo "Docker is required" >&2; exit 1; }
  docker compose version >/dev/null 2>&1 || { echo "Docker Compose v2 is required" >&2; exit 1; }
  root=/opt/dispatch-edge
  install -d -m 0755 "$root/image"
  install -m 0755 "$temporary" "$root/image/dispatch-agent"
  cat > "$root/image/Containerfile" <<'EOF'
FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY dispatch-agent /usr/local/bin/dispatch-agent
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/dispatch-agent"]
EOF
  cat > "$root/compose.yaml" <<EOF
services:
  edge:
    build: ./image
    restart: unless-stopped
    env_file:
      - $config_root/edge.env
    read_only: true
    security_opt:
      - no-new-privileges:true
    tmpfs:
      - /tmp
EOF
  docker compose -f "$root/compose.yaml" up -d --build --remove-orphans
  docker compose -f "$root/compose.yaml" ps
  exit 0
fi

[ "$install_mode" = systemd ] || { echo "Install mode must be docker or systemd" >&2; exit 1; }
command -v systemctl >/dev/null 2>&1 || { echo "systemd is required" >&2; exit 1; }
install -m 0755 "$temporary" /usr/local/bin/dispatch-agent
cat > /etc/systemd/system/dispatch-edge.service <<'EOF'
[Unit]
Description=Dispatch outbound edge node
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/dispatch-edge/edge.env
ExecStart=/usr/local/bin/dispatch-agent
Restart=always
RestartSec=3
DynamicUser=true
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now dispatch-edge
systemctl --no-pager --full status dispatch-edge | sed -n '1,12p'
`

func (a *API) edgeInstallScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = io.WriteString(w, edgeInstaller)
}

func (a *API) edgeBinary(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(strings.TrimSpace(chi.URLParam(r, "platform")))
	if name != "linux-amd64" && name != "linux-arm64" {
		http.NotFound(w, r)
		return
	}
	root := strings.TrimSpace(os.Getenv("DISPATCH_EDGE_BINARY_ROOT"))
	if root == "" {
		root = "/usr/local/lib/dispatch-edge"
	}
	path := filepath.Join(root, name)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="dispatch-agent"`)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, path)
}

func (a *API) authenticateEdge(r *http.Request) (core.PrivateNetwork, bool) {
	item, err := a.store.GetPrivateNetwork(r.Context(), chi.URLParam(r, "id"))
	if err != nil || item.Driver != edge.DriverAgent || item.TokenHash == "" {
		return item, false
	}
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	hash := sha256.Sum256([]byte(provided))
	encoded := base64.RawURLEncoding.EncodeToString(hash[:])
	return item, subtle.ConstantTimeCompare([]byte(encoded), []byte(item.TokenHash)) == 1
}

func (a *API) touchEdgeNode(ctx context.Context, item core.PrivateNetwork, r *http.Request) {
	now := time.Now().UTC()
	if item.Details == nil {
		item.Details = map[string]string{}
	}
	item.Details["lastSeenAt"] = now.Format(time.RFC3339Nano)
	if version := strings.TrimSpace(r.Header.Get("X-Dispatch-Agent-Version")); version != "" {
		item.Details["version"] = version
	}
	item.State, item.LastVerifiedAt, item.UpdatedAt = "ready", &now, now
	_ = a.store.UpdatePrivateNetwork(ctx, item)
}

func (a *API) leaseEdgeJob(w http.ResponseWriter, r *http.Request) {
	item, ok := a.authenticateEdge(r)
	if !ok {
		problem(w, http.StatusUnauthorized, "Authentication required", "The edge node credential is invalid.")
		return
	}
	if a.edge == nil {
		problem(w, http.StatusServiceUnavailable, "Edge routing unavailable", "The controller is not configured for edge jobs.")
		return
	}
	a.touchEdgeNode(r.Context(), item, r)
	deadline := time.Now().Add(25 * time.Second)
	for {
		job, err := a.edge.Lease(r.Context(), item.ID)
		if err != nil {
			a.internal(w, err)
			return
		}
		if job != nil {
			writeJSON(w, http.StatusOK, job)
			return
		}
		if time.Now().After(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(400 * time.Millisecond):
		}
	}
}

func (a *API) completeEdgeJob(w http.ResponseWriter, r *http.Request) {
	item, ok := a.authenticateEdge(r)
	if !ok {
		problem(w, http.StatusUnauthorized, "Authentication required", "The edge node credential is invalid.")
		return
	}
	if a.edge == nil {
		problem(w, http.StatusServiceUnavailable, "Edge routing unavailable", "The controller is not configured for edge jobs.")
		return
	}
	var completion edge.Completion
	if !decode(w, r, &completion) {
		return
	}
	if err := a.edge.Complete(r.Context(), item.ID, chi.URLParam(r, "jobId"), completion); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "Job not found", "The edge job expired or was already handled.")
			return
		}
		a.internal(w, err)
		return
	}
	a.touchEdgeNode(r.Context(), item, r)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) rotateEdgeToken(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetPrivateNetwork(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Edge node")
		return
	}
	if item.Driver != edge.DriverAgent {
		problem(w, http.StatusBadRequest, "Not an edge node", "Only managed edge nodes have enrollment tokens.")
		return
	}
	token, hash, err := newEdgeToken()
	if err != nil {
		a.internal(w, err)
		return
	}
	item.TokenHash, item.EnrollmentToken, item.State, item.UpdatedAt = hash, token, "waiting", time.Now().UTC()
	if err := a.store.UpdatePrivateNetwork(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
