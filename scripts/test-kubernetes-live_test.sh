#!/usr/bin/env bash
# Cleanup regression checks. All cluster tools are fixtures; no Docker API calls.
set -euo pipefail
cd "$(dirname "$0")/.."
fixture=$(mktemp -d "${TMPDIR:-/tmp}/dispatch-kube-script.XXXXXXXX")
mkdir "$fixture/bin"
cleanup() {
  for file in "$fixture"/bin/* "$fixture"/*.log "$fixture"/owner "$fixture"/container "$fixture"/calls; do
    [[ ! -f $file ]] || rm "$file"
  done
  rmdir "$fixture/bin" "$fixture"
}
trap cleanup EXIT
cat > "$fixture/bin/go" <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
cat > "$fixture/bin/docker" <<'MOCK'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$FIXTURE/calls"
case "$1 $2" in
  'volume create') printf '%s\n' "${4#dispatch.test.owner=}" > "$FIXTURE/owner" ;;
  'volume inspect')
    [[ ${3:-} == -f ]] || exit 1
    if [[ $SCENARIO == wrong-owner ]]; then echo someone-else; else cat "$FIXTURE/owner"; fi ;;
  'volume rm') [[ $SCENARIO != failed-volume ]] ;;
  'container inspect') [[ -f $FIXTURE/container ]] ;;
  'inspect -f') cat "$FIXTURE/owner" ;;
  'port '*) echo 127.0.0.1:12345 ;;
  'run -d') touch "$FIXTURE/container" ;;
  'stop --timeout') ;;
  'rm -v') rm "$FIXTURE/container" ;;
  'exec '*)
    if [[ ${3:-} == cat ]]; then printf 'server: https://127.0.0.1:6443\n'; fi ;;
  *) echo "Unexpected fixture invocation: $*" >&2; exit 3 ;;
esac
MOCK
chmod +x "$fixture/bin/go" "$fixture/bin/docker"

for scenario in success failed-volume wrong-owner; do
  : > "$fixture/calls"
  result=0
  PATH="$fixture/bin:$PATH" FIXTURE="$fixture" SCENARIO="$scenario" \
    DOCKER_CONTEXT= DOCKER_HOST=unix:///fixture/docker.sock DISPATCH_TEST_KUBERNETES_ISOLATED=1 scripts/test-kubernetes-live.sh upgrade > "$fixture/$scenario.log" 2>&1 || result=$?
  if [[ $scenario == success ]]; then
    [[ $result == 0 ]] || { cat "$fixture/$scenario.log"; exit 1; }
  else
    [[ $result != 0 ]] || { echo "Cleanup failure incorrectly succeeded: $scenario" >&2; exit 1; }
    grep -q 'retained volume dispatch-k3s-data-' "$fixture/$scenario.log"
    if [[ $scenario == wrong-owner ]] && grep -q '^volume rm ' "$fixture/calls"; then
      echo 'Ownership failure attempted volume deletion' >&2
      exit 1
    fi
  fi
  printf 'PASS: %s\n' "$scenario"
done

: > "$fixture/calls"
result=0
PATH="$fixture/bin:$PATH" FIXTURE="$fixture" SCENARIO=success DOCKER_CONTEXT= DOCKER_HOST=ssh://example.invalid \
  DISPATCH_TEST_KUBERNETES_ISOLATED=1 scripts/test-kubernetes-live.sh upgrade > "$fixture/remote.log" 2>&1 || result=$?
[[ $result == 2 && ! -s $fixture/calls ]] || { echo 'Remote endpoint was not refused before Docker access' >&2; exit 1; }
printf 'PASS: remote endpoint refusal\n'
