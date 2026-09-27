#!/usr/bin/env bash
set -euo pipefail

# LaunchAgent entrypoint for the tailnet-facing YNAB HTTP gateway. It binds
# loopback only; `tailscale serve` is the sole path in from the tailnet. It
# shares the stdio process's state directory so both draw on one quota budget.
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
env_file="${MCP_GATEWAY_YNAB_ENV_FILE:-$repo_root/.env.ynab.local}"
fail() { printf 'personal-mcp-gateway: %s\n' "$1" >&2; exit 1; }

# shellcheck source=internal/release-config.sh
source "$script_dir/internal/release-config.sh" >/dev/null 2>&1 || fail "local environment configuration is invalid."
[[ -f "$env_file" ]] && load_release_config "$env_file" || fail "local environment configuration is invalid."
unset OPENAI_API_KEY CONTROL_PLANE_API_KEY
for name in GATEWAY_BIN YNAB_TOKEN MCP_GATEWAY_ALLOWED_HOST; do [[ -n "${!name:-}" && "${!name}" != *"..."* ]] || fail "$name is required in the configured local environment file."; done
[[ -x "$GATEWAY_BIN" ]] || fail "configured GATEWAY_BIN is not executable."
[[ -z "${YNAB_EXPORT_ROOT:-}" || "$YNAB_EXPORT_ROOT" = /* ]] || fail "YNAB_EXPORT_ROOT must be an absolute directory when configured."

addr="127.0.0.1:8768"
state_dir="${MCP_GATEWAY_STATE_DIR:-$HOME/Library/Application Support/personal-mcp-gateway/ynab}"
telemetry_db="${MCP_GATEWAY_TELEMETRY_DB:-$state_dir/telemetry.sqlite}"
health_url_file="/tmp/personal-mcp-gateway/ynab-http-health.url"
mkdir -p -- "$(dirname -- "$telemetry_db")" "$(dirname -- "$health_url_file")"
printf 'http://%s\n' "$addr" >"$health_url_file"

export YNAB_TOKEN YNAB_EXPORT_ROOT
exec "$GATEWAY_BIN" http \
  --server ynab \
  --addr "$addr" \
  --allowed-host "$MCP_GATEWAY_ALLOWED_HOST" \
  --telemetry-db "$telemetry_db"
