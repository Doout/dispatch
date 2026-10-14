#!/bin/sh
# Connection JSON arrives on stdin, separate from this script.
set -eu

fail() { printf '%s\n' "$1" >&2; exit 1; }
case "$(uname -s)" in Linux) ;; *) fail 'Doodship requires a Linux server.' ;; esac
case "$(uname -m)" in
  x86_64|aarch64|arm64) ;;
  *) fail 'Doodship supports amd64 and arm64 servers.' ;;
esac
command -v docker >/dev/null 2>&1 || fail 'Install Docker on this server, then run this command again.'
if [ -n "${DOCKER_CONTEXT:-}" ]; then
  endpoint=$(docker context inspect "$DOCKER_CONTEXT" --format '{{(index .Endpoints "docker").Host}}')
else
  endpoint=${DOCKER_HOST:-$(docker context inspect --format '{{(index .Endpoints "docker").Host}}')}
fi
[ "$endpoint" = unix:///var/run/docker.sock ] || fail 'Select the local Docker engine at /var/run/docker.sock, then run this command again.'
docker info >/dev/null 2>&1 || fail 'Docker is not running or your user cannot access it.'
[ -n "${HOME:-}" ] || fail 'HOME must point to your home directory.'
case "$HOME" in /*) ;; *) fail 'HOME must be an absolute path.' ;; esac
directory=$HOME/.doodship
[ ! -L "$directory" ] || fail 'The installation directory must not be a symbolic link.'
umask 077
mkdir -p "$directory"
chmod 700 "$directory"
image=${DOODSHIP_IMAGE:-ghcr.io/doout/dispatch:main}
exec docker run --rm -i --user 0 --network host \
  --mount "type=bind,src=$directory,dst=$directory" \
  --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
  "$image" setup --dir "$directory" --name doodship \
  --image "$image" --host-uid "$(id -u)" --host-gid "$(id -g)" \
  "$@" --connection-stdin
