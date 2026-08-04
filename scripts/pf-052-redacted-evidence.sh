#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 --binary /absolute/yuanshu [--server-config /absolute/server.toml] [--node-config /absolute/node.toml]" >&2
  exit 2
}

binary=""
server_config=""
node_config=""
while (($#)); do
  case "$1" in
    --binary) binary=${2:-}; shift 2 ;;
    --server-config) server_config=${2:-}; shift 2 ;;
    --node-config) node_config=${2:-}; shift 2 ;;
    *) usage ;;
  esac
done
[[ "$binary" = /* && -x "$binary" ]] || usage

echo "PF-052 redacted environment"
echo "Commit: $(git rev-parse --verify HEAD)"
echo "Platform: $(uname -s) $(uname -m)"
echo "OS: $(sw_vers -productVersion 2>/dev/null || uname -r)"
echo "Codex: $(codex --version 2>/dev/null | head -1 || echo unavailable)"
echo "Go: $(go version 2>/dev/null | sed 's#go version ##' || echo unavailable)"
echo "Node.js: $(node --version 2>/dev/null || echo unavailable)"
echo "pnpm: $(pnpm --version 2>/dev/null || echo unavailable)"

redact_json() {
  local kind=$1
  python3 -c '
import json, sys
kind = sys.argv[1]
try:
    value = json.load(sys.stdin)
except Exception:
    raise SystemExit("doctor did not return valid JSON")
if kind == "server":
    keys = ("version", "state", "config", "tls", "tlsExpiryWarning", "backup", "backupLastAt", "backupSizeBytes", "web", "admin")
else:
    keys = ("version", "state", "platform", "config", "identity", "database", "workspaces", "codex", "authentication", "recovery", "remoteControl", "relayLastError", "compatibility", "workspaceStatus", "credential", "autostart")
print(json.dumps({key: value[key] for key in keys if key in value}, ensure_ascii=False, indent=2))
' "$kind"
}

if [[ -n "$server_config" ]]; then
  echo "Server doctor (redacted):"
  set +e
  server_output=$($binary server doctor --config "$server_config" --json 2>/dev/null)
  server_code=$?
  set -e
  printf '%s' "$server_output" | redact_json server
  echo "Server doctor exit: $server_code"
fi

if [[ -n "$node_config" ]]; then
  echo "Node doctor (redacted):"
  set +e
  node_output=$($binary node doctor --config "$node_config" --json 2>/dev/null)
  node_code=$?
  set -e
  printf '%s' "$node_output" | redact_json node
  echo "Node doctor exit: $node_code"
fi
