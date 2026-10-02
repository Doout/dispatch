#!/usr/bin/env bash
# Creates only isolated mock containers/volumes; never uses cloud credentials.
set -euo pipefail
cd "$(dirname "$0")/.."
export DISPATCH_PROVIDER_IMAGE="${DISPATCH_PROVIDER_IMAGE:-dispatch-provider-mock:test}"
export DISPATCH_CONTAINER="dispatch-provider-smoke-${RANDOM}-$$"
export DISPATCH_PROVIDER_TOKEN=
export DISPATCH_PROVIDER_FAULT=
sidecar_project="${DISPATCH_CONTAINER}-sidecar"
compose=(docker compose -p "$sidecar_project" -f examples/providers/sidecar/compose.yaml)
cleanup() {
  "${compose[@]}" down --volumes >/dev/null 2>&1 || true
  docker rm -f "$DISPATCH_CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT
# The anchor supplies the same network namespace a controller container would.
docker run -d --name "$DISPATCH_CONTAINER" --network none "$DISPATCH_PROVIDER_IMAGE" --listen 127.0.0.1:8092 >/dev/null
"${compose[@]}" up -d
conformance() {
  docker run --rm --network "container:${DISPATCH_CONTAINER}" \
    --entrypoint /usr/local/bin/dispatch-provider-conformance "$DISPATCH_PROVIDER_IMAGE" \
    --allow-mutations --timeout 15s --poll-interval 10ms
}
ready() {
  for attempt in $(seq 1 15); do
    if conformance >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}
ready
for failure in incompatible unavailable; do
  export DISPATCH_PROVIDER_FAULT="$failure"
  "${compose[@]}" up -d --force-recreate
  if conformance >/dev/null 2>&1; then
    echo "Broken sidecar unexpectedly passed conformance: $failure" >&2
    exit 1
  fi
  export DISPATCH_PROVIDER_FAULT=
  "${compose[@]}" up -d --force-recreate
  ready
done
conformance
