#!/bin/sh
set -eu
repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM
mkdir -p "$scratch/bin" "$scratch/assets" "$scratch/target" "$scratch/payload"
printf '#!/bin/sh\necho test-dispatch\n' > "$scratch/payload/dispatch"
chmod +x "$scratch/payload/dispatch"
case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) exit 0 ;; esac
archive=dispatch-linux-$arch.tar.gz
tar -czf "$scratch/assets/$archive" -C "$scratch/payload" dispatch
(cd "$scratch/assets" && sha256sum "$archive" > SHA256SUMS)
cat > "$scratch/bin/curl" <<'CURL'
#!/bin/sh
set -eu
url= output=
while [ "$#" -gt 0 ]; do
 case "$1" in
  https://*) url=$1 ;;
  -o) shift; output=$1 ;;
 esac
 shift
done
cp "$DISPATCH_TEST_ASSETS/${url##*/}" "$output"
CURL
chmod +x "$scratch/bin/curl"
export DISPATCH_TEST_ASSETS="$scratch/assets"
export DISPATCH_BIN_DIR="$scratch/target"
export PATH="$scratch/bin:$PATH"
sh "$repo/scripts/install.sh" > "$scratch/output"
test "$("$scratch/target/dispatch")" = test-dispatch
printf 'tampered' >> "$scratch/assets/$archive"
if sh "$repo/scripts/install.sh" > "$scratch/output" 2>&1; then
 echo 'Installer accepted a corrupt archive' >&2
 exit 1
fi
test "$("$scratch/target/dispatch")" = test-dispatch
echo 'Bootstrap checksum validation and existing-binary preservation passed.'
