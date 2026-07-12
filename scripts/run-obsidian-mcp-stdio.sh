#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
env_file="${MCP_GATEWAY_ENV_FILE:-$repo_root/.env.local}"

fail() {
  printf 'personal-mcp-gateway: %s\n' "$1" >&2
  exit 1
}

# shellcheck source=internal/release-config.sh
if ! source "$script_dir/internal/release-config.sh" >/dev/null 2>&1; then
  fail "local environment configuration is invalid."
fi
if [[ -f "$env_file" ]] && ! load_release_config "$env_file"; then
  fail "local environment configuration is invalid."
fi

if [[ -z "${OBSIDIAN_ROOT:-}" ]]; then
  fail "OBSIDIAN_ROOT is required in the configured local environment file."
fi

if [[ ! -d "$OBSIDIAN_ROOT" ]]; then
  fail "OBSIDIAN_ROOT does not exist or is not a directory."
fi

state_dir="${MCP_GATEWAY_STATE_DIR:-$HOME/Library/Application Support/personal-mcp-gateway}"
telemetry_db="${MCP_GATEWAY_TELEMETRY_DB:-$state_dir/telemetry.sqlite}"
mkdir -p -- "$(dirname -- "$telemetry_db")"

if [[ -n "${GATEWAY_BIN:-}" ]]; then
  if [[ ! -x "$GATEWAY_BIN" ]]; then
    fail "configured GATEWAY_BIN is not executable."
  fi
  exec "$GATEWAY_BIN" stdio \
    --obsidian-root "$OBSIDIAN_ROOT" \
    --telemetry-db "$telemetry_db"
fi

cd "$repo_root"
exec go run ./cmd/gateway stdio \
  --obsidian-root "$OBSIDIAN_ROOT" \
  --telemetry-db "$telemetry_db"
