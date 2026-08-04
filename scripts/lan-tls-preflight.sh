#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 --public-url https://HOST:PORT --cert /absolute/server.crt --key /absolute/server.key --origin https://HOST:PORT" >&2
  exit 2
}

public_url=""
certificate=""
private_key=""
origin=""
while (($#)); do
  case "$1" in
    --public-url) public_url=${2:-}; shift 2 ;;
    --cert) certificate=${2:-}; shift 2 ;;
    --key) private_key=${2:-}; shift 2 ;;
    --origin) origin=${2:-}; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$public_url" && -n "$certificate" && -n "$private_key" && -n "$origin" ]] || usage
[[ "$certificate" = /* && "$private_key" = /* ]] || { echo "certificate and key paths must be absolute" >&2; exit 1; }
[[ -f "$certificate" && ! -L "$certificate" && -f "$private_key" && ! -L "$private_key" ]] || { echo "certificate or key is unavailable, or is a symbolic link" >&2; exit 1; }

python3 - "$public_url" "$certificate" "$origin" <<'PY'
import ipaddress
import ssl
import sys
from urllib.parse import urlsplit

public_url, certificate, origin = sys.argv[1:]
public = urlsplit(public_url)
allowed = urlsplit(origin)
if public.scheme != "https" or not public.hostname or public.username or public.password or public.query or public.fragment:
    raise SystemExit("public URL must be a clean HTTPS origin")
if allowed.scheme != "https" or not allowed.hostname or allowed.username or allowed.password or allowed.query or allowed.fragment:
    raise SystemExit("allowed control origin must be HTTPS")

decoded = ssl._ssl._test_decode_cert(certificate)
identities = []
for kind, value in decoded.get("subjectAltName", []):
    if kind in {"DNS", "IP Address"}:
        identities.append(value)
host = public.hostname
try:
    wanted = ipaddress.ip_address(host)
    matched = any(kind == "IP Address" and ipaddress.ip_address(value) == wanted for kind, value in decoded.get("subjectAltName", []))
except ValueError:
    matched = any(kind == "DNS" and value.lower() == host.lower() for kind, value in decoded.get("subjectAltName", []))
if not matched:
    raise SystemExit("certificate SAN does not match the public URL host")

ssl.match_hostname(decoded, host)
print("LAN TLS preflight: certificate identity and HTTPS origins are valid")
print(f"Public endpoint: https://{public.hostname}:{public.port or 443}")
print(f"Certificate expires: {decoded.get('notAfter', 'unknown')}")
print(f"SAN entries: {len(identities)}")
PY

key_mode=$(stat -f '%Lp' "$private_key" 2>/dev/null || stat -c '%a' "$private_key")
if [[ "$key_mode" != "600" && "$key_mode" != "400" ]]; then
  echo "private key permissions must be 0600 or 0400 (current: $key_mode)" >&2
  exit 1
fi

cert_public=$(mktemp)
key_public=$(mktemp)
trap 'rm -f "$cert_public" "$key_public"' EXIT
openssl x509 -in "$certificate" -pubkey -noout >"$cert_public"
openssl pkey -in "$private_key" -pubout >"$key_public"
cmp -s "$cert_public" "$key_public" || { echo "certificate and private key do not match" >&2; exit 1; }
echo "LAN TLS preflight: certificate and private key match; key permissions are private"
