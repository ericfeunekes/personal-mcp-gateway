#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
env_file="${MCP_GATEWAY_ENV_FILE:-$repo_root/.env.local}"

fail() {
  printf 'personal-mcp-gateway verification: %s\n' "$1" >&2
  exit 1
}

# shellcheck source=internal/release-config.sh
if ! source "$script_dir/internal/release-config.sh" >/dev/null 2>&1; then
  fail "local environment configuration is invalid."
fi
if [[ -f "$env_file" ]] && ! load_release_config "$env_file"; then
  fail "local environment configuration is invalid."
fi

# Health verification never needs runtime credentials. Keep them out of curl,
# launchctl, and any diagnostics those commands may emit.
unset CONTROL_PLANE_API_KEY OPENAI_API_KEY

label="${LAUNCHD_LABEL:-com.ericfeunekes.personal-mcp-gateway.obsidian-tunnel}"
health_url_file="${TUNNEL_HEALTH_URL_FILE:-/tmp/personal-mcp-gateway/tunnel-health.url}"
timeout_seconds="${RELEASE_READY_TIMEOUT_SECONDS:-45}"
poll_seconds="${RELEASE_READY_POLL_SECONDS:-1}"
uid="$(id -u)"

if ! [[ "$timeout_seconds" =~ ^[1-9][0-9]*$ ]]; then
  fail "RELEASE_READY_TIMEOUT_SECONDS must be a positive integer."
fi
if ! [[ "$poll_seconds" =~ ^(0\.[0-9]*[1-9][0-9]*|[1-9][0-9]*(\.[0-9]+)?)$ ]]; then
  fail "RELEASE_READY_POLL_SECONDS must be a positive number."
fi

launch_state="$(launchctl print "gui/$uid/$label" 2>/dev/null)" ||
  fail "the tunnel LaunchAgent is not loaded."
if [[ -z "$launch_state" ]]; then
  fail "the tunnel LaunchAgent returned no state."
fi
expected_program="$repo_root/scripts/run-obsidian-tunnel.sh"
if ! printf '%s\n' "$launch_state" | grep -Fq -- "program = $expected_program"; then
  fail "the loaded LaunchAgent does not use this repo's tunnel wrapper."
fi

deadline=$((SECONDS + timeout_seconds))
while (( SECONDS < deadline )); do
  if [[ -s "$health_url_file" ]]; then
    health_url="$(head -n 1 "$health_url_file")"
    if [[ "$health_url" =~ ^http://127\.0\.0\.1:[0-9]+/?$ ]]; then
      health_url="${health_url%/}"
      if curl --max-time 2 --fail --silent --show-error "$health_url/healthz" >/dev/null 2>&1 &&
        curl --max-time 2 --fail --silent --show-error "$health_url/readyz" >/dev/null 2>&1; then
        printf 'live verification passed: LaunchAgent loaded, tunnel live, tunnel ready\n'
        exit 0
      fi
    fi
  fi
  sleep "$poll_seconds"
done

fail "the tunnel did not become live and ready before the bounded timeout."
