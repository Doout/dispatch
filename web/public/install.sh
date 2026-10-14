#!/bin/sh
# Host checks belong here. The CLI owns installation state, accounts and recovery.
# Connection JSON arrives on stdin and is read only by the final setup command.
set -eu

fail() { printf '%s\n' "$1" >&2; exit 1; }

usage() {
  cat <<'HELP'
Usage: sh install.sh [setup options] < connection.json

Requires Linux amd64 or arm64, a local Docker engine and 2 GiB of free Docker storage.
Needs 512 MiB free on the home filesystem and compatible glibc/libstdc++6 libraries.
Installs under $HOME/.doodship. Compose runs inside the installer image.

  --name NAME           Compose project name (default: doodship)
  --port PORT           Loopback port (default: 8080)
  --auto-update=false   Disable daily Docker updates
  --pull=false          Use an existing local image without pulling
  --timeout DURATION    Controller startup timeout (default: 90s)

Set DOODSHIP_IMAGE to select another controller image.
The CLI also accepts --data-volume, --bind, --public-url, --proxy-network,
--hostname and --tls-resolver. Run the installed CLI's setup --help for details.
HELP
}

check_options() {
  bootstrap_pull=always
  bootstrap_auto_update=true
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --help|-h) usage; exit 0 ;;
      --pull=false) bootstrap_pull=never ;;
      --pull|--pull=true) bootstrap_pull=always ;;
      --auto-update|--auto-update=true) bootstrap_auto_update=true ;;
      --auto-update=false) bootstrap_auto_update=false ;;
      --name|--port|--timeout|--data-volume|--bind|--public-url|--proxy-network|--hostname|--tls-resolver)
        [ "$#" -ge 2 ] || fail 'A setup option is missing its value. Run with --help for options.'
        shift ;;
      --name=*|--port=*|--timeout=*|--data-volume=*|--bind=*|--public-url=*|--proxy-network=*|--hostname=*|--tls-resolver=*) ;;
      *) fail 'Unsupported setup option. Run with --help for options.' ;;
    esac
    shift
  done
}

check_tools() {
  for tool in uname id mkdir chmod stat df awk mktemp rm rmdir docker; do
    command -v "$tool" >/dev/null 2>&1 || fail "Missing $tool. Install it and run this command again."
  done
}

check_platform() {
  [ "$(uname -s)" = Linux ] || fail 'Doodship requires a Linux server.'
  case "$(uname -m)" in
    x86_64) bootstrap_arch=amd64 ;;
    aarch64|arm64) bootstrap_arch=arm64 ;;
    *) fail 'Doodship supports amd64 and arm64 servers.' ;;
  esac
}

# Explicit --host keeps every Docker command on the engine we checked.
local_docker() { docker --host unix:///var/run/docker.sock "$@"; }

check_docker() {
  if [ -n "${DOCKER_CONTEXT:-}" ]; then
    bootstrap_endpoint=$(docker context inspect "$DOCKER_CONTEXT" --format '{{(index .Endpoints "docker").Host}}' </dev/null) || fail 'Cannot read the selected Docker context.'
  elif [ -n "${DOCKER_HOST:-}" ]; then
    bootstrap_endpoint=$DOCKER_HOST
  else
    bootstrap_endpoint=$(docker context inspect --format '{{(index .Endpoints "docker").Host}}' </dev/null) || fail 'Cannot read the selected Docker context.'
  fi
  [ "$bootstrap_endpoint" = unix:///var/run/docker.sock ] || fail 'Select the local Docker engine at /var/run/docker.sock, then run this command again.'
  bootstrap_server=$(local_docker info --format '{{.OSType}}/{{.Architecture}}' </dev/null 2>/dev/null) || fail 'Docker is not running or your user cannot access it. Check docker info.'
  case "$bootstrap_server" in
    linux/x86_64|linux/amd64) bootstrap_server_arch=amd64 ;;
    linux/aarch64|linux/arm64) bootstrap_server_arch=arm64 ;;
    *) fail 'The Docker engine must run Linux on amd64 or arm64.' ;;
  esac
  [ "$bootstrap_server_arch" = "$bootstrap_arch" ] || fail 'The Docker engine architecture does not match this server.'
}

check_directory() {
  [ -n "${HOME:-}" ] || fail 'HOME must point to your home directory.'
  case "$HOME" in /*) ;; *) fail 'HOME must be an absolute path.' ;; esac
  [ -d "$HOME" ] && [ -w "$HOME" ] && [ -x "$HOME" ] || fail 'Your home directory must exist and be writable.'
  bootstrap_home=$(CDPATH='' cd -P "$HOME" && pwd -P) || fail 'Cannot resolve your home directory.'
  bootstrap_directory=$bootstrap_home/.doodship
  bootstrap_cr=$(printf '\r')
  case "$bootstrap_directory" in
    *:*|*,*|*\"*|*\$*|*%*|*"$bootstrap_cr"*|*'
'*) fail 'The installation path contains characters that Docker or Compose cannot use.' ;;
  esac
  [ ! -L "$bootstrap_directory" ] || fail 'The installation directory must not be a symbolic link.'
  if [ -e "$bootstrap_directory" ]; then
    [ -d "$bootstrap_directory" ] || fail 'The installation path is already used by a file.'
    bootstrap_owner=$(stat -c '%u' "$bootstrap_directory") || fail 'Cannot read the installation directory owner.'
    [ "$bootstrap_owner" = "$(id -u)" ] || fail 'Run setup as the user who owns the installation directory.'
    [ -w "$bootstrap_directory" ] && [ -x "$bootstrap_directory" ] || fail 'The installation directory is not writable.'
  fi
}

check_free_space() {
  bootstrap_available=$(df -Pk "$1" | awk 'NR == 2 && $4 ~ /^[0-9]+$/ {print $4}')
  [ -n "$bootstrap_available" ] || fail 'Cannot check available disk space.'
  [ "$bootstrap_available" -ge "$2" ] || fail "$3"
}

check_storage() {
  bootstrap_storage=$(local_docker info --format '{{.DockerRootDir}}' </dev/null) || fail 'Cannot locate Docker storage.'
  [ -n "$bootstrap_storage" ] && [ -d "$bootstrap_storage" ] || fail 'Docker storage must be on this server. Remote engines and Docker Desktop are not supported.'
  check_free_space "$bootstrap_storage" 2097152 'Docker storage needs at least 2 GiB free. Free space and retry.'
  check_free_space "$HOME" 524288 'Your home filesystem needs at least 512 MiB free for the CLI and its updates.'
}

prepare_image() {
  bootstrap_image=${DOODSHIP_IMAGE:-ghcr.io/doout/dispatch:main}
  case "$bootstrap_image" in
    ''|[!a-zA-Z0-9]*|*[!a-zA-Z0-9._/:@-]*) fail 'DOODSHIP_IMAGE is not a valid image reference.' ;;
  esac
  case "$bootstrap_image" in
    *@sha256:*) [ "$bootstrap_auto_update" = false ] || fail 'A digest-pinned image requires --auto-update=false.' ;;
  esac
  if [ "$bootstrap_pull" = always ]; then
    local_docker pull "$bootstrap_image" </dev/null || fail 'Cannot download the installer image. Check registry access and free disk space, then retry.'
  fi
  bootstrap_image_id=$(local_docker image inspect --format '{{.Id}}' "$bootstrap_image" </dev/null) || fail 'The installer image is not available locally.'
  case "$bootstrap_image_id" in sha256:*) ;; *) fail 'Docker did not return an installer image ID.' ;; esac
  bootstrap_image_platform=$(local_docker image inspect --format '{{.Os}}/{{.Architecture}}' "$bootstrap_image_id" </dev/null) || fail 'Cannot read the installer image platform.'
  [ "$bootstrap_image_platform" = "linux/$bootstrap_arch" ] || fail 'The installer image does not match this server architecture.'
  # Check the image's client against this daemon, rather than guessing a Docker version.
  local_docker run --rm --pull=never --network none --user 0 \
    --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
    --entrypoint docker "$bootstrap_image_id" info >/dev/null </dev/null || fail 'The installer image cannot use this Docker engine. Update Docker and retry.'
  bootstrap_compose=$(local_docker run --rm --pull=never --network none --entrypoint docker \
    "$bootstrap_image_id" compose version --short </dev/null) || fail 'The installer image is missing Docker Compose.'
  printf '%s\n' "$bootstrap_compose" | awk -F. ' {sub(/^v/, "", $1); if ($1 !~ /^[0-9]+$/ || $2 !~ /^[0-9]+$/ || $1 < 2 || ($1 == 2 && $2 < 30)) exit 1}' || fail 'The installer image requires Docker Compose 2.30 or newer.'
  local_docker run --rm --pull=never --network none --entrypoint /usr/local/bin/dispatch \
    "$bootstrap_image_id" setup --help >/dev/null </dev/null || fail 'This image does not support Doodship setup. Select a current controller image.'
}

cleanup_cli_check() {
  if [ -n "${bootstrap_scratch:-}" ]; then
    if [ -e "$bootstrap_scratch/dispatch" ] || [ -L "$bootstrap_scratch/dispatch" ]; then
      rm -- "$bootstrap_scratch/dispatch"
    fi
    rmdir "$bootstrap_scratch"
    bootstrap_scratch=
  fi
}

check_native_cli() {
  bootstrap_scratch=$(mktemp -d "$HOME/.doodship-check.XXXXXXXX") || fail 'Cannot create a private CLI check directory.'
  trap 'cleanup_cli_check' 0
  trap 'exit 1' HUP INT TERM
  local_docker run --rm --pull=never --network none --user 0 \
    --mount "type=bind,src=$bootstrap_scratch,dst=/host-check" \
    --entrypoint sh "$bootstrap_image_id" -c 'cp /usr/local/bin/dispatch /host-check/dispatch && chmod 755 /host-check/dispatch' </dev/null || fail 'Cannot copy the CLI for its compatibility check.'
  [ -f "$bootstrap_scratch/dispatch" ] && [ ! -L "$bootstrap_scratch/dispatch" ] || fail 'The image must contain a regular CLI binary.'
  "$bootstrap_scratch/dispatch" version >/dev/null </dev/null || fail 'This host cannot run the CLI. It requires compatible glibc and libstdc++6 libraries.'
  cleanup_cli_check
  trap - 0 HUP INT TERM
}

prepare_directory() {
  umask 077
  if ! mkdir -p "$bootstrap_directory" || ! chmod 700 "$bootstrap_directory"; then
    fail 'Cannot create the private installation directory.'
  fi
}

start_setup() {
  exec docker --host unix:///var/run/docker.sock run --rm --pull=never -i --user 0 --network host \
    --mount "type=bind,src=$bootstrap_directory,dst=$bootstrap_directory" \
    --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
    "$bootstrap_image_id" setup --name doodship "$@" --dir "$bootstrap_directory" \
    --image "$bootstrap_image" --host-uid "$(id -u)" --host-gid "$(id -g)" --connection-stdin
}

main() {
  check_options "$@"
  check_tools
  check_platform
  check_docker
  check_directory
  check_storage
  prepare_image
  check_native_cli
  prepare_directory
  start_setup "$@"
}
main "$@"
