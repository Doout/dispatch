#!/usr/bin/env bash
# Install a standalone Dispatch CLI.
set -euo pipefail

usage() {
  cat <<'HELP'
Usage: install-cli.sh [--doodshipctl] [--version vX.Y.Z | --list-versions]

With no arguments, install the latest stable doodship release.
--doodshipctl installs the automation API client for CI jobs and agents.
--version accepts a release number with or without the leading v.
--list-versions lists stable releases available for this platform (requires jq).
Set DOODSHIP_BIN_DIR to change the install directory (default: ~/.local/bin).
HELP
}

fail() { printf '%s\n' "$*" >&2; exit 1; }
fetch() {
  if command -v curl >/dev/null; then
    curl --proto '=https' --proto-redir '=https' -fsSL "$1"
  else
    wget -qO- "$1"
  fi
}

tool=doodship
if [[ ${1:-} == --doodshipctl ]]; then
  tool=doodshipctl
  shift
fi

case "${1:-}" in
  -h|--help) usage; exit 0 ;;
  # doodship update passes the resolved path of the running executable and its build version.
  --update-executable)
    [[ $tool == doodship && $# == 3 && $2 == /* && -f $2 ]] || fail "Expected an installed executable."
    mode=update
    target=$2
    current_version=$3
    ;;
  --list-versions) [[ $# == 1 ]] || { usage >&2; exit 2; }; mode=list ;;
  --version) [[ $# == 2 ]] || { usage >&2; exit 2; }; mode=pinned ;;
  *) [[ $# == 0 ]] || { usage >&2; exit 2; }; mode=latest ;;
esac

case "$(uname -s)" in
  Darwin) os=darwin ;; Linux) os=linux ;; *) fail "Supported systems: macOS and Linux." ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) fail "Supported architectures: arm64 and amd64." ;;
esac
asset="$tool-$os-$arch.tar.gz"
releases=https://github.com/Doout/dispatch/releases

command -v curl >/dev/null || command -v wget >/dev/null || fail "Install curl or wget to download $tool."

if [[ $mode == list ]]; then
  command -v jq >/dev/null || fail "Install jq to use --list-versions."
  page=1
  while :; do
    response=$(fetch "https://api.github.com/repos/Doout/dispatch/releases?per_page=100&page=$page")
    jq -r --arg asset "$asset" '.[] | select(.draft == false and .prerelease == false)
      | select(.assets | any(.name == $asset) and any(.name == "SHA256SUMS"))
      | .tag_name | select(test("^v[0-9]+\\.[0-9]+\\.[0-9]+$"))' <<< "$response"
    [[ $(jq length <<< "$response") == 100 ]] || break
    page=$((page + 1))
  done
  exit 0
fi

if [[ $mode != pinned ]]; then
  if command -v curl >/dev/null; then
    url=$(curl -fsSLI --proto '=https' --proto-redir '=https' -o /dev/null -w '%{url_effective}' "$releases/latest")
  else
    # GitHub redirects /latest to the stable release's tag page.
    url=$(wget --spider --server-response "$releases/latest" 2>&1 |
      awk -v prefix="$releases/tag/" 'tolower($1) == "location:" && index($2, prefix) == 1 { print $2 }')
  fi
  version=${url##*/}
else
  version="v${2#v}"
fi
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "Expected a stable release tag (for example, v0.2.0)."

if [[ $mode == update ]]; then
  if [[ $version == "$current_version" ]]; then
    printf 'doodship %s is already up to date.\n' "$version"
    exit 0
  fi
  bin_dir=$(dirname "$target")
else
  bin_dir=${DOODSHIP_BIN_DIR:-"$HOME/.local/bin"}
  mkdir -p "$bin_dir"
  target="$bin_dir/$tool"
fi
[[ ! -d "$target" ]] || fail "$target is a directory."
tmp=$(mktemp -d "$bin_dir/.doodship-install.XXXXXXXX")
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' HUP INT TERM
base="$releases/download/$version"
fetch "$base/$asset" > "$tmp/$asset" || fail "Could not download $asset for $version. Use --list-versions to see available versions."
fetch "$base/SHA256SUMS" > "$tmp/SHA256SUMS"
(
  cd "$tmp"
  awk -v asset="$asset" '$2 == asset { print; matches++ } END { exit matches != 1 }' SHA256SUMS > expected.sha256 ||
    fail "Missing or duplicate checksum for $asset."
  if command -v sha256sum >/dev/null; then
    sha256sum -c expected.sha256
  else
    shasum -a 256 -c expected.sha256
  fi
  tar -xzf "$asset" "$tool"
  [[ -f $tool && ! -L $tool ]] || fail "Expected a $tool executable in $asset."
  chmod 755 "$tool"
)
mv -f "$tmp/$tool" "$target"
if [[ $mode == update ]]; then
  printf 'Updated %s to doodship %s.\n' "$target" "$version"
  exit 0
fi
printf 'Installed %s %s to %s\n' "$tool" "$version" "$target"
case ":${PATH:-}:" in
  *":$bin_dir:"*) ;;
  *) printf 'Add this to your shell profile and run it in this terminal:\n  export PATH=%q:"$PATH"\n' "$bin_dir" ;;
esac
