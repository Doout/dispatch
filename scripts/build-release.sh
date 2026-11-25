#!/bin/sh
set -eu
source_dir=$(pwd)
version=${1:-development}
output=${2:-release}
case "$version" in *[!a-zA-Z0-9._-]*|'') echo 'Invalid release version.' >&2; exit 1 ;; esac
native_ldflags="-s -w -linkmode=external -extldflags=-static -X github.com/doout/dispatch/internal/installation.Version=$version"
mkdir -p "$output/edge" "$output/relay"
for arch in amd64 arm64; do
  mkdir -p "$output/$arch"
  case "$arch" in
    amd64) compiler=x86_64-linux-gnu-gcc; cpp_compiler=x86_64-linux-gnu-g++ ;;
    arm64) compiler=aarch64-linux-gnu-gcc; cpp_compiler=aarch64-linux-gnu-g++ ;;
  esac
  CGO_ENABLED=1 CC="$compiler" CXX="$cpp_compiler" GOOS=linux GOARCH="$arch" go build -trimpath -tags netgo,osusergo -ldflags="$native_ldflags" -o "$output/$arch/dispatch" ./cmd/dispatch
  CGO_ENABLED=1 CC="$compiler" CXX="$cpp_compiler" GOOS=linux GOARCH="$arch" go build -trimpath -tags netgo,osusergo -ldflags="$native_ldflags" -o "$output/$arch/dispatch-platform" ./cmd/dispatch-platform
  for binary in dispatch dispatch-platform; do
    elf=$(LC_ALL=C readelf -lWd "$output/$arch/$binary")
    if printf '%s\n' "$elf" | grep -Eq '(^[[:space:]]*INTERP[[:space:]]|\(NEEDED\))'; then
      echo "$binary ($arch) requires a dynamic loader or shared library" >&2
      exit 1
    fi
  done
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w" -o "$output/$arch/dispatch-worker" ./cmd/dispatch-worker
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w" -o "$output/$arch/dispatch-hook" ./cmd/dispatch-hook
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w" -o "$output/edge/linux-$arch" ./cmd/dispatch-agent
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w" -o "$output/relay/linux-$arch" ./cmd/dispatch-relay
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w" -o "$output/$arch/doodshipctl" ./cmd/doodshipctl
done
for arch in amd64 arm64; do
  mkdir -p "$output/darwin-$arch"
  CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build -trimpath -ldflags="-s -w" -o "$output/darwin-$arch/doodshipctl" ./cmd/doodshipctl
  tar -czf "$output/doodshipctl-darwin-$arch.tar.gz" -C "$output/darwin-$arch" doodshipctl -C "$source_dir" LICENSE
  tar -czf "$output/doodshipctl-linux-$arch.tar.gz" -C "$output/$arch" doodshipctl -C "$source_dir" LICENSE
  cp "$output/$arch/doodshipctl" "$output/$arch/dispatchctl"
  tar -czf "$output/dispatch-linux-$arch.tar.gz" -C "$output/$arch" dispatch dispatchctl -C "$source_dir" LICENSE -C "$source_dir/web/public/fonts" OFL-Manrope.txt
  tar -czf "$output/dispatch-platform-linux-$arch.tar.gz" -C "$output/$arch" dispatch-platform dispatch-worker -C "$(cd "$output" && pwd)" edge -C "$source_dir" LICENSE -C "$source_dir/web/public/fonts" OFL-Manrope.txt
done
sh scripts/build-doodship-release.sh "$output" "$version"
(cd "$output" && sha256sum dispatch-linux-*.tar.gz dispatch-platform-linux-*.tar.gz doodship-*.tar.gz doodshipctl-*.tar.gz > SHA256SUMS)
