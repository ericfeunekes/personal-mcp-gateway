#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
env_file="${MCP_GATEWAY_ENV_FILE:-$repo_root/.env.local}"

if [[ -f "$env_file" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$env_file"
  set +a
fi

fail() {
  printf 'personal-mcp-gateway: %s\n' "$1" >&2
  exit 1
}

if [[ -z "${OBSIDIAN_ROOT:-}" ]]; then
  fail "OBSIDIAN_ROOT is required. Set it in $env_file."
fi

if [[ ! -d "$OBSIDIAN_ROOT" ]]; then
  fail "OBSIDIAN_ROOT does not exist or is not a directory: $OBSIDIAN_ROOT"
fi

state_dir="${MCP_GATEWAY_STATE_DIR:-$HOME/Library/Application Support/personal-mcp-gateway}"
telemetry_db="${MCP_GATEWAY_TELEMETRY_DB:-$state_dir/telemetry.sqlite}"
mkdir -p -- "$(dirname -- "$telemetry_db")"

if [[ -n "${GATEWAY_BIN:-}" ]]; then
  if [[ ! -x "$GATEWAY_BIN" ]]; then
    fail "GATEWAY_BIN is set but is not executable: $GATEWAY_BIN"
  fi
  exec "$GATEWAY_BIN" stdio \
    --obsidian-root "$OBSIDIAN_ROOT" \
    --telemetry-db "$telemetry_db"
fi

cd "$repo_root"
exec go run ./cmd/gateway stdio \
  --obsidian-root "$OBSIDIAN_ROOT" \
  --telemetry-db "$telemetry_db"
