#!/usr/bin/env bash
set -euo pipefail

# LaunchAgent entrypoint for the tailnet-facing Obsidian HTTP gateway. It binds
# loopback only; `tailscale serve` is the sole path in from the tailnet.
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
env_file="${MCP_GATEWAY_ENV_FILE:-$repo_root/.env.local}"
fail() { printf 'personal-mcp-gateway: %s\n' "$1" >&2; exit 1; }

# shellcheck source=internal/release-config.sh
source "$script_dir/internal/release-config.sh" >/dev/null 2>&1 || fail "local environment configuration is invalid."
[[ -f "$env_file" ]] && load_release_config "$env_file" || fail "local environment configuration is invalid."
unset OPENAI_API_KEY CONTROL_PLANE_API_KEY
for name in GATEWAY_BIN OBSIDIAN_ROOT MCP_GATEWAY_ALLOWED_HOST; do [[ -n "${!name:-}" && "${!name}" != *"..."* ]] || fail "$name is required in the configured local environment file."; done
[[ -x "$GATEWAY_BIN" ]] || fail "configured GATEWAY_BIN is not executable."
[[ -d "$OBSIDIAN_ROOT" ]] || fail "OBSIDIAN_ROOT does not exist or is not a directory."

addr="127.0.0.1:8765"
state_dir="${MCP_GATEWAY_STATE_DIR:-$HOME/Library/Application Support/personal-mcp-gateway}"
telemetry_db="${MCP_GATEWAY_TELEMETRY_DB:-$state_dir/telemetry.sqlite}"
health_url_file="/tmp/personal-mcp-gateway/obsidian-http-health.url"
mkdir -p -- "$(dirname -- "$telemetry_db")" "$(dirname -- "$health_url_file")"
printf 'http://%s\n' "$addr" >"$health_url_file"

exec "$GATEWAY_BIN" http \
  --addr "$addr" \
  --allowed-host "$MCP_GATEWAY_ALLOWED_HOST" \
  --obsidian-root "$OBSIDIAN_ROOT" \
  --telemetry-db "$telemetry_db"
