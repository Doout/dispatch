package bootstrap

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"text/template"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
)

// The script has fixed actions. Every interpolated value is carried in a private
// JSON file rather than shell syntax, and credentials never appear in argv.
func renderScript(item core.TargetBootstrap, claim string) (string, error) {
	data, err := json.Marshal(map[string]any{"id": item.ID, "nodeId": item.NodeID, "claim": claim, "plan": item.Plan})
	if err != nil {
		return "", err
	}
	input := struct {
		ID, Config                      string
		InstallRuntime, ReplaceIdentity bool
	}{item.ID, base64.StdEncoding.EncodeToString(data), item.Plan.InstallRuntime, item.Plan.ReplaceIdentity}
	var result bytes.Buffer
	err = installerTemplate.Execute(&result, input)
	return result.String(), err
}
func renderCloudInit(item core.TargetBootstrap, claim string) (string, error) {
	script, err := renderScript(item, claim)
	if err != nil {
		return "", err
	}
	path := "/var/lib/dispatch-bootstrap/" + item.ID + ".sh"
	config := map[string]any{"write_files": []map[string]any{{"path": path, "permissions": "0700", "owner": "root:root", "encoding": "b64", "content": base64.StdEncoding.EncodeToString([]byte(script))}}, "runcmd": []any{[]string{"timeout", "--signal=TERM", "--kill-after=10s", "14m", "sh", path}}}
	raw, err := yaml.Marshal(config)
	return "#cloud-config\n" + string(raw), err
}

var installerTemplate = template.Must(template.New("agent-installer").Parse(`#!/bin/sh
set -eu
umask 077
command -v python3 >/dev/null 2>&1 || { echo 'Python 3 is required for the approved installer' >&2; exit 1; }
command -v systemctl >/dev/null 2>&1 || { echo 'systemd is required for the approved installer' >&2; exit 1; }
[ "$(id -u)" = 0 ] || { echo 'The approved installer requires root' >&2; exit 1; }
install -d -m 0700 /var/lib/dispatch-bootstrap
bootstrap_root=/var/lib/dispatch-bootstrap/{{.ID}}
install -d -m 0700 "$bootstrap_root"
printf '%s' '{{.Config}}' | base64 -d > "$bootstrap_root/config.json"
# One installer for this intended target at a time, including cloud-init retries.
exec 9>/var/lib/dispatch-bootstrap/install.lock
flock -w 30 9 || { echo 'Another target installer is active' >&2; exit 1; }
cat > "$bootstrap_root/client.py" <<'PYTHON'
import json, os, sys, urllib.request, urllib.error, time, ssl
root = os.path.dirname(__file__)
with open(root + '/config.json') as source: config = json.load(source)
plan = config['plan']
base = plan['controllerUrl'] + '/api/v1/bootstrap/claims/' + config['id']
def request(action, data):
    body = json.dumps(data).encode()
    req = urllib.request.Request(base + action, data=body, method='POST', headers={'Authorization': 'Bearer '+config['claim'], 'Content-Type':'application/json'})
    # urllib never forwards the bootstrap bearer token to redirected endpoints.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs): return None
    opener = urllib.request.build_opener(NoRedirect)
    with opener.open(req, timeout=10) as response:
        return json.loads(response.read(8192))
if sys.argv[1] == 'progress':
    try: request('/progress', {'phase':sys.argv[2]})
    except Exception: pass
elif sys.argv[1] == 'identity':
    path='/var/lib/dispatch-edge/identity.json'
    if os.path.exists(path):
        with open(path) as source: identity=json.load(source)
        if identity.get('controller')!=plan['controllerUrl'] or identity.get('nodeId')!=config['nodeId']:
            sys.exit('Existing identity belongs to another target or controller')
elif sys.argv[1] == 'download':
    import hashlib
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs): return None
    with urllib.request.build_opener(NoRedirect).open(plan['artifactUrl'],timeout=30) as response:
        data=response.read(134217729)
    if not data or len(data)>134217728 or hashlib.sha256(data).hexdigest()!=plan['artifactSha256']:
        sys.exit('The pinned agent artifact did not match its reviewed SHA-256')
    with open(root+'/agent','wb') as target: target.write(data)
elif sys.argv[1] == 'enroll':
    for attempt in range(120):
        try:
            result=request('',{})
            if result.get('state') in ('enroll','enrolled'):
                if result.get('nodeId') != config['nodeId']: sys.exit('Enrollment target mismatch')
                token=result.get('token','')
                if token and (len(token)>128 or any(c not in 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-' for c in token)): sys.exit('Enrollment token invalid')
                values={'DISPATCH_EDGE_CONTROLLER_URL':plan['controllerUrl'],'DISPATCH_EDGE_NODE_ID':config['nodeId'],'DISPATCH_EDGE_TOKEN':token,'DISPATCH_EDGE_IDENTITY_FILE':'/var/lib/dispatch-edge/identity.json','DISPATCH_AGENT_RUNTIME':'true','DISPATCH_AGENT_RUNTIME_STATE':'/var/lib/dispatch-edge/runtime'}
                with open(root+'/edge.env','w') as target:
                    for key,value in values.items(): target.write(key+'='+value+'\n')
                break
        except Exception: pass
        time.sleep(5)
    else: sys.exit('Enrollment is unavailable; retry the reviewed installer for this target')
PYTHON
chmod 0600 "$bootstrap_root/client.py"
python3 "$bootstrap_root/client.py" identity
python3 "$bootstrap_root/client.py" progress downloading
trap 'python3 "$bootstrap_root/client.py" progress failed; echo "Target installation interrupted; review bootstrap status" >&2' EXIT
trap 'exit 1' HUP INT TERM
{{if .InstallRuntime}}
. /etc/os-release
[ "$ID" = ubuntu ] && [ "$VERSION_ID" = 24.04 ] || { echo 'Approved prerequisites require Ubuntu 24.04' >&2; exit 1; }
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates curl git docker.io docker-compose-v2
systemctl enable --now docker
{{end}}
docker info >/dev/null 2>&1 || { echo 'Docker daemon is unavailable' >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo 'Docker Compose v2 is unavailable' >&2; exit 1; }
git --version >/dev/null 2>&1 || { echo 'Git is unavailable' >&2; exit 1; }
python3 "$bootstrap_root/client.py" download
python3 "$bootstrap_root/client.py" progress installing
# A private identity is retained during ordinary upgrades. Replacement is an
# explicit reviewed action and its local marker prevents resetting twice.
python3 "$bootstrap_root/client.py" enroll
install -d -m 0700 /var/lib/dispatch-edge
install -d -m 0755 /etc/dispatch-edge
systemctl stop dispatch-edge.service 2>/dev/null || true
{{if .ReplaceIdentity}}
if [ ! -f "$bootstrap_root/identity-replaced" ]; then
    rm -f /var/lib/dispatch-edge/identity.json
    : > "$bootstrap_root/identity-replaced"
fi
{{end}}
install -m 0755 "$bootstrap_root/agent" /usr/local/bin/dispatch-agent.new
mv -f /usr/local/bin/dispatch-agent.new /usr/local/bin/dispatch-agent
install -m 0600 "$bootstrap_root/edge.env" /etc/dispatch-edge/edge.env
cat > /etc/systemd/system/dispatch-edge.service <<'UNIT'
[Unit]
Description=Dispatch enrolled target agent
After=network-online.target docker.service
Wants=network-online.target
[Service]
Type=simple
EnvironmentFile=/etc/dispatch-edge/edge.env
ExecStart=/usr/local/bin/dispatch-agent
Restart=always
RestartSec=5
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now dispatch-edge.service
python3 "$bootstrap_root/client.py" progress installed
rm -f "$bootstrap_root/agent" "$bootstrap_root/edge.env"
trap - EXIT HUP INT TERM
echo 'Agent installed; readiness requires authenticated controller verification'
`))
