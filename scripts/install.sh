#!/bin/sh
# Download the Dispatch CLI. Docker is installed separately by the operator.
set -eu

case "$(uname -s)" in Linux) ;; *) echo 'Dispatch installation currently requires Linux.' >&2; exit 1 ;; esac
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo 'Supported architectures: amd64 and arm64.' >&2; exit 1 ;;
esac
version=${DISPATCH_VERSION:-latest}
case "$version" in latest) release=latest/download ;; v[0-9]* )
  case "$version" in *[!a-zA-Z0-9._-]*) echo 'Invalid DISPATCH_VERSION.' >&2; exit 1 ;; esac
  release=download/$version ;;
  *) echo 'DISPATCH_VERSION must be latest or a v-prefixed release tag.' >&2; exit 1 ;;
esac
bin_dir=${DISPATCH_BIN_DIR:-/usr/local/bin}
archive=dispatch-linux-$arch.tar.gz
base=https://github.com/Doout/dispatch/releases/$release
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 "$base/$archive" -o "$scratch/$archive"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 "$base/SHA256SUMS" -o "$scratch/SHA256SUMS"
(cd "$scratch" && awk -v file="$archive" '$2 == file { print }' SHA256SUMS > selected.sha256 && test -s selected.sha256 && sha256sum -c selected.sha256)
tar -xzf "$scratch/$archive" -C "$scratch" dispatch
if [ -w "$bin_dir" ] || [ "$(id -u)" = 0 ]; then
  install -d "$bin_dir"
  install -m 0755 "$scratch/dispatch" "$bin_dir/dispatch"
else
  sudo install -d "$bin_dir"
  sudo install -m 0755 "$scratch/dispatch" "$bin_dir/dispatch"
fi
printf 'CLI installed at %s/dispatch\nNext: sudo %s/dispatch install\n' "$bin_dir" "$bin_dir"
