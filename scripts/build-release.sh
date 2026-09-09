#!/bin/sh
set -eu
source_dir=$(pwd)
version=${1:-development}
output=${2:-release}
case "$version" in *[!a-zA-Z0-9._-]*|'') echo 'Invalid release version.' >&2; exit 1 ;; esac
mkdir -p "$output/edge" "$output/relay"
for arch in amd64 arm64; do
  mkdir -p "$output/$arch"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w -X github.com/doout/dispatch/internal/installation.Version=$version" -o "$output/$arch/dispatch" ./cmd/dispatch
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w" -o "$output/$arch/dispatch-hook" ./cmd/dispatch-hook
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w" -o "$output/edge/linux-$arch" ./cmd/dispatch-agent
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w" -o "$output/relay/linux-$arch" ./cmd/dispatch-relay
  tar -czf "$output/dispatch-linux-$arch.tar.gz" -C "$output/$arch" dispatch -C "$source_dir" LICENSE -C "$source_dir/web/public/fonts" OFL-Manrope.txt
done
(cd "$output" && sha256sum dispatch-linux-*.tar.gz > SHA256SUMS)
