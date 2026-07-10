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

# This tunnel profile uses CONTROL_PLANE_API_KEY explicitly. Do not let a
# broader inherited OpenAI API key become an accidental fallback credential.
unset OPENAI_API_KEY

fail() {
  printf 'personal-mcp-gateway: %s\n' "$1" >&2
  exit 1
}

require_env() {
  local name="$1"
  local value="${!name:-}"
  if [[ -z "$value" ]]; then
    fail "$name is required in the configured local environment file."
  fi
  if [[ "$value" == *"..."* ]]; then
    fail "$name still contains a placeholder value in the configured local environment file."
  fi
}

require_env CONTROL_PLANE_TUNNEL_ID
require_env CONTROL_PLANE_API_KEY
require_env OBSIDIAN_ROOT

if [[ ! -d "$OBSIDIAN_ROOT" ]]; then
  fail "OBSIDIAN_ROOT does not exist or is not a directory."
fi

tunnel_client="$repo_root/tools/tunnel-client/tunnel-client"
if [[ ! -x "$tunnel_client" ]]; then
  fail "repo-local tunnel client is missing or not executable."
fi

mcp_command="$repo_root/scripts/run-obsidian-mcp-stdio.sh"
if [[ ! -x "$mcp_command" ]]; then
  fail "repo-local MCP stdio wrapper is missing or not executable."
fi

profile_name="${TUNNEL_CLIENT_PROFILE:-obsidian-stdio}"
profile_dir="${TUNNEL_CLIENT_PROFILE_DIR:-${TMPDIR:-/tmp}/personal-mcp-gateway/tunnel-client-profiles}"
health_url_file="${TUNNEL_HEALTH_URL_FILE:-${TMPDIR:-/tmp}/personal-mcp-gateway/tunnel-health.url}"
health_listen_addr="${TUNNEL_HEALTH_LISTEN_ADDR:-127.0.0.1:0}"
log_format="${TUNNEL_LOG_FORMAT:-json}"

mkdir -p -- "$profile_dir" "$(dirname -- "$health_url_file")"

"$tunnel_client" init \
  --sample sample_mcp_stdio_local \
  --profile "$profile_name" \
  --profile-dir "$profile_dir" \
  --tunnel-id "$CONTROL_PLANE_TUNNEL_ID" \
  --mcp-command "$mcp_command" \
  --control-plane-api-key-ref env:CONTROL_PLANE_API_KEY \
  --health-listen-addr "$health_listen_addr" \
  --force >/dev/null

printf 'Starting OpenAI tunnel profile %s\n' "$profile_name" >&2
printf 'Health URL file: %s\n' "$health_url_file" >&2

exec "$tunnel_client" run \
  --profile "$profile_name" \
  --profile-dir "$profile_dir" \
  --health.url-file "$health_url_file" \
  --log.format "$log_format"
