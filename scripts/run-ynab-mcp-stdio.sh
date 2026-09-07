#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
env_file="${MCP_GATEWAY_YNAB_ENV_FILE:-$repo_root/.env.ynab.local}"

fail() { printf 'personal-mcp-gateway: %s\n' "$1" >&2; exit 1; }

# shellcheck source=internal/release-config.sh
source "$script_dir/internal/release-config.sh" >/dev/null 2>&1 || fail "local environment configuration is invalid."
[[ -f "$env_file" ]] && load_release_config "$env_file" || fail "local environment configuration is invalid."
[[ -n "${YNAB_TOKEN:-}" ]] || fail "YNAB_TOKEN is required in the configured local environment file."
[[ -z "${YNAB_EXPORT_ROOT:-}" || "$YNAB_EXPORT_ROOT" = /* ]] || fail "YNAB_EXPORT_ROOT must be an absolute directory when configured."

state_dir="${MCP_GATEWAY_STATE_DIR:-$HOME/Library/Application Support/personal-mcp-gateway/ynab}"
telemetry_db="${MCP_GATEWAY_TELEMETRY_DB:-$state_dir/telemetry.sqlite}"
mkdir -p -- "$(dirname -- "$telemetry_db")"

if [[ -n "${GATEWAY_BIN:-}" ]]; then
  [[ -x "$GATEWAY_BIN" ]] || fail "configured GATEWAY_BIN is not executable."
  export YNAB_TOKEN YNAB_EXPORT_ROOT
  exec "$GATEWAY_BIN" stdio --server ynab --telemetry-db "$telemetry_db"
fi
cd "$repo_root"
export YNAB_TOKEN YNAB_EXPORT_ROOT
exec go run ./cmd/gateway stdio --server ynab --telemetry-db "$telemetry_db"
