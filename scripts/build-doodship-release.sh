#!/bin/sh
set -eu
output=${1:?release output directory required}
version=${2:-development}
for os in darwin linux; do
  for arch in amd64 arm64; do
    name="doodship-$os-$arch"
    mkdir -p "$output/$name"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags="-s -w -X main.releaseVersion=$version" -o "$output/$name/doodship" ./cmd/doodship
    tar -czf "$output/$name.tar.gz" -C "$output/$name" doodship -C "$(pwd)" LICENSE
  done
done
