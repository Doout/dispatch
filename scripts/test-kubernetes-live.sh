#!/usr/bin/env bash
# Run only against Docker clusters created by this script. Never use KUBECONFIG.
set -euo pipefail

if [[ ${DISPATCH_TEST_KUBERNETES_ISOLATED:-} != 1 ]]; then
  echo 'Set DISPATCH_TEST_KUBERNETES_ISOLATED=1 to create disposable local Docker clusters.' >&2
  exit 2
fi
mode=${1:-all}
case "$mode" in all|kind|upgrade) ;; *) echo 'Usage: scripts/test-kubernetes-live.sh [all|kind|upgrade]' >&2; exit 2 ;; esac
for tool in docker go python3; do command -v "$tool" >/dev/null; done
# An explicit Docker context overrides DOCKER_HOST, matching the Docker CLI.
if [[ -n ${DOCKER_HOST:-} && -z ${DOCKER_CONTEXT:-} ]]; then
  docker_endpoint=$DOCKER_HOST
else
  docker_endpoint=$(docker context inspect "$(docker context show)" --format '{{.Endpoints.docker.Host}}')
fi
case "$docker_endpoint" in
  unix://*) ;;
  *) echo 'This runner requires a local Docker Unix socket; remote Docker endpoints are refused.' >&2; exit 2 ;;
esac
kind_bin=${KIND:-kind}
if [[ $mode != upgrade ]]; then command -v "$kind_bin" >/dev/null; fi
cd "$(dirname "$0")/.."
# Sequential clusters avoid multiplying container runtime and watcher usage.
private_dir=$(mktemp -d "${TMPDIR:-/tmp}/dispatch-kube.XXXXXXXX")
chmod 700 "$private_dir"
suffix="$(date +%s)-$$"
owner="dispatch-live-$suffix"
k3s_name="dispatch-k3s-$suffix"
kind_name="dispatch-kind-$suffix"
volume="dispatch-k3s-data-$suffix"
k3s_created=0
kind_created=0
volume_created=0
k3s_port=''
kind_image='kindest/node:v1.36.4@sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed'
k3s_before='rancher/k3s:v1.35.5-k3s1@sha256:2074403abe1bded11ef3dde09d457e13be8e0b64c218b1c4f8269b4565cfbc65'
k3s_after='rancher/k3s:v1.36.2-k3s1@sha256:6a47cea22c4b834d4ba72c89d291696b79ebe406251f90b446e4dff03513dd87'

remove_k3s() {
  if [[ $k3s_created == 1 ]] && docker container inspect "$k3s_name" >/dev/null 2>&1; then
    if [[ $(docker inspect -f '{{index .Config.Labels "dispatch.test.owner"}}' "$k3s_name") != "$owner" ]]; then
      echo "Ownership check failed; retained container $k3s_name" >&2
      return 1
    fi
    if ! docker stop --timeout 30 "$k3s_name" >/dev/null || ! docker rm -v "$k3s_name" >/dev/null; then
      echo "Cleanup failed; retained container $k3s_name" >&2
      return 1
    fi
  fi
  k3s_created=0
}
remove_kind() {
  if [[ $kind_created == 1 ]]; then
    if ! KIND_EXPERIMENTAL_PROVIDER=docker "$kind_bin" delete cluster --name "$kind_name" --kubeconfig "$private_dir/kind.yaml"; then
      echo "Cleanup failed; retained Kind cluster $kind_name" >&2
      return 1
    fi
  fi
  kind_created=0
}
cleanup() {
  local result=$? cleanup_failed=0
  trap - EXIT
  set +e
  remove_kind || cleanup_failed=1
  remove_k3s || cleanup_failed=1
  if [[ $volume_created == 1 ]]; then
    if [[ $(docker volume inspect -f '{{index .Labels "dispatch.test.owner"}}' "$volume") != "$owner" ]]; then
      echo "Ownership check failed; retained volume $volume" >&2
      cleanup_failed=1
    elif ! docker volume rm "$volume" >/dev/null; then
      echo "Cleanup failed; retained volume $volume" >&2
      cleanup_failed=1
    fi
  fi
  # Remove only known private files, never a caller-supplied directory.
  for file in kind.yaml k3s.yaml upgrade.json; do
    if [[ -f "$private_dir/$file" ]] && ! rm "$private_dir/$file"; then cleanup_failed=1; fi
  done
  if ! rmdir "$private_dir"; then
    echo "Cleanup failed; retained private directory $private_dir" >&2
    cleanup_failed=1
  fi
  if [[ $result == 0 && $cleanup_failed != 0 ]]; then result=1; fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

run_go() {
  env -u DISPATCH_TEST_KUBERNETES_UPGRADE_PHASE -u DISPATCH_TEST_KUBERNETES_UPGRADE_RECORD GOMAXPROCS="${GOMAXPROCS:-2}" GOFLAGS="${GOFLAGS:--p=1}" \
    DISPATCH_TEST_KUBERNETES_ISOLATED=1 "$@" go test -race ./internal/deploy \
    -run '^TestKubernetes(Lifecycle|Upgrade)Integration$' -count=1 -v
}
start_k3s() {
  local image=$1
  local bind="127.0.0.1:${k3s_port}:6443"
  if docker container inspect "$k3s_name" >/dev/null 2>&1; then echo 'Refusing existing container name' >&2; exit 1; fi
  k3s_created=1
  docker run -d --name "$k3s_name" --hostname "$k3s_name" --label "dispatch.test.owner=$owner" \
    --privileged -p "$bind" -v "$volume:/var/lib/rancher/k3s" "$image" \
    server --node-name "$k3s_name" --disable traefik --disable servicelb --disable metrics-server --write-kubeconfig-mode 600 >/dev/null
  local ready=0
  for ((attempt=0; attempt<90; attempt++)); do
    if docker exec "$k3s_name" kubectl get --raw=/readyz >/dev/null 2>&1; then ready=1; break; fi
    sleep 2
  done
  [[ $ready == 1 ]] || { echo 'Disposable K3s API did not become ready' >&2; exit 1; }
  docker exec "$k3s_name" kubectl wait --for=create "node/$k3s_name" --timeout=180s
  docker exec "$k3s_name" kubectl wait --for=condition=Ready "node/$k3s_name" --timeout=180s
  docker exec "$k3s_name" kubectl -n kube-system wait --for=create deployment/local-path-provisioner --timeout=180s
  docker exec "$k3s_name" kubectl -n kube-system rollout status deployment/local-path-provisioner --timeout=180s
  k3s_port=$(docker port "$k3s_name" 6443/tcp)
  k3s_port=${k3s_port##*:}
  docker exec "$k3s_name" cat /etc/rancher/k3s/k3s.yaml | python3 -c '
import os, sys
path, port = sys.argv[1:]
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w") as out:
    out.write(sys.stdin.read().replace("127.0.0.1:6443", "127.0.0.1:" + port))
' "$private_dir/k3s.yaml" "$k3s_port"
}

if [[ $mode != upgrade ]]; then
  if docker container inspect "$kind_name-control-plane" >/dev/null 2>&1; then echo 'Refusing existing Kind node' >&2; exit 1; fi
  kind_created=1
  KIND_EXPERIMENTAL_PROVIDER=docker "$kind_bin" create cluster --name "$kind_name" --image "$kind_image" --kubeconfig "$private_dir/kind.yaml" --wait 180s
  # Node Ready alone does not prove that the storage provisioner can reach the API.
  docker exec "$kind_name-control-plane" kubectl --kubeconfig=/etc/kubernetes/admin.conf -n kube-system rollout status daemonset/kube-proxy --timeout=180s
  docker exec "$kind_name-control-plane" kubectl --kubeconfig=/etc/kubernetes/admin.conf -n local-path-storage rollout status deployment/local-path-provisioner --timeout=180s
  run_go DISPATCH_TEST_KUBECONFIG="$private_dir/kind.yaml"
  remove_kind
fi
if [[ $mode != kind ]]; then
  if docker volume inspect "$volume" >/dev/null 2>&1; then echo 'Refusing existing data volume' >&2; exit 1; fi
  docker volume create --label "dispatch.test.owner=$owner" "$volume" >/dev/null
  volume_created=1
  start_k3s "$k3s_before"
  run_go DISPATCH_TEST_KUBECONFIG="$private_dir/k3s.yaml" \
    DISPATCH_TEST_KUBERNETES_UPGRADE_PHASE=before DISPATCH_TEST_KUBERNETES_UPGRADE_RECORD="$private_dir/upgrade.json"
  remove_k3s
  start_k3s "$k3s_after"
  run_go DISPATCH_TEST_KUBECONFIG="$private_dir/k3s.yaml" \
    DISPATCH_TEST_KUBERNETES_UPGRADE_PHASE=after DISPATCH_TEST_KUBERNETES_UPGRADE_RECORD="$private_dir/upgrade.json"
fi
