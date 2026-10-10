#!/bin/sh
# Read-only external checks. Run from outside the installation network.
set -eu
if [ "$#" -lt 1 ]; then
  echo 'Usage: check-hosted.sh HTTPS_ORIGIN [NAMESERVER_IP ...]' >&2
  exit 2
fi
origin=${1%/}
shift
case "$origin" in https://*) ;; *) echo 'Use an HTTPS origin.' >&2; exit 2 ;; esac
host=${origin#https://}
case "$host" in ''|*[!a-zA-Z0-9.-]*) echo 'Use a DNS hostname without a path or port.' >&2; exit 2 ;; esac
curl --fail --silent --show-error --max-time 10 "$origin/readyz" >/dev/null
echo 'HTTPS and catalog readiness passed.'
cert=$(mktemp)
trap 'rm -f "$cert"' EXIT HUP INT TERM
timeout 10 openssl s_client -connect "$host:443" -servername "$host" </dev/null 2>/dev/null |
  openssl x509 -out "$cert"
openssl x509 -in "$cert" -noout -checkend 1209600 >/dev/null || {
  echo 'Console certificate expires within 14 days.' >&2
  exit 1
}
echo 'Console certificate has at least 14 days remaining.'
for server in "$@"; do
  case "$server" in ''|*[!a-fA-F0-9:.]*) echo 'Nameservers must be IP addresses.' >&2; exit 2 ;; esac
  for transport in +notcp +tcp; do
    answer=$(dig "@$server" "$host" SOA +norecurse "$transport" +time=3 +tries=1)
    printf '%s\n' "$answer" | grep -q 'status: NOERROR' || { echo "DNS failed at $server $transport" >&2; exit 1; }
    printf '%s\n' "$answer" | grep -Eq 'flags: [^;]*\baa\b' || { echo "DNS answer is not authoritative at $server $transport" >&2; exit 1; }
    printf '%s\n' "$answer" | grep -Eq '[[:space:]]SOA[[:space:]]' || { echo "DNS SOA is missing at $server $transport" >&2; exit 1; }
  done
  echo "Authoritative DNS passed over UDP and TCP at $server."
done
