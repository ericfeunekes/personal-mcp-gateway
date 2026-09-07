#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
env_file="${MCP_GATEWAY_YNAB_ENV_FILE:-$repo_root/.env.ynab.local}"
fail() { printf 'personal-mcp-gateway: %s\n' "$1" >&2; exit 1; }

# shellcheck source=internal/release-config.sh
source "$script_dir/internal/release-config.sh" >/dev/null 2>&1 || fail "local environment configuration is invalid."
[[ -f "$env_file" ]] && load_release_config "$env_file" || fail "local environment configuration is invalid."
unset OPENAI_API_KEY
for name in CONTROL_PLANE_TUNNEL_ID CONTROL_PLANE_API_KEY YNAB_TOKEN; do [[ -n "${!name:-}" && "${!name}" != *"..."* ]] || fail "$name is required in the configured local environment file."; done
[[ -z "${YNAB_EXPORT_ROOT:-}" || "$YNAB_EXPORT_ROOT" = /* ]] || fail "YNAB_EXPORT_ROOT must be an absolute directory when configured."
tunnel_client="$repo_root/tools/tunnel-client/tunnel-client"
mcp_command="$repo_root/scripts/run-ynab-mcp-stdio.sh"
[[ -x "$tunnel_client" && -x "$mcp_command" ]] || fail "repo-local tunnel runtime is missing or not executable."
profile_name="${TUNNEL_CLIENT_PROFILE:-ynab-stdio}"
profile_dir="${TUNNEL_CLIENT_PROFILE_DIR:-${TMPDIR:-/tmp}/personal-mcp-gateway/ynab-tunnel-client-profiles}"
health_url_file="${TUNNEL_HEALTH_URL_FILE:-/tmp/personal-mcp-gateway/ynab-tunnel-health.url}"
health_listen_addr="${TUNNEL_HEALTH_LISTEN_ADDR:-127.0.0.1:0}"
log_format="${TUNNEL_LOG_FORMAT:-json}"
mkdir -p -- "$profile_dir" "$(dirname -- "$health_url_file")"
"$tunnel_client" init --sample sample_mcp_stdio_local --profile "$profile_name" --profile-dir "$profile_dir" --tunnel-id "$CONTROL_PLANE_TUNNEL_ID" --mcp-command "$mcp_command" --control-plane-api-key-ref env:CONTROL_PLANE_API_KEY --health-listen-addr "$health_listen_addr" --force >/dev/null
exec "$tunnel_client" run --profile "$profile_name" --profile-dir "$profile_dir" --health.url-file "$health_url_file" --log.format "$log_format"
