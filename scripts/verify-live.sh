#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"

fail() {
  printf 'personal-mcp-gateway verification: %s\n' "$1" >&2
  exit 1
}

server="${SERVER:-obsidian}"
case "$server" in
  obsidian) env_file="${MCP_GATEWAY_ENV_FILE:-$repo_root/.env.local}"; default_label="com.ericfeunekes.personal-mcp-gateway.obsidian-tunnel"; default_health="/tmp/personal-mcp-gateway/tunnel-health.url" ;;
  ynab) env_file="${MCP_GATEWAY_YNAB_ENV_FILE:-$repo_root/.env.ynab.local}"; default_label="com.ericfeunekes.personal-mcp-gateway.ynab-tunnel"; default_health="/tmp/personal-mcp-gateway/ynab-tunnel-health.url" ;;
  obsidian-http) env_file="${MCP_GATEWAY_ENV_FILE:-$repo_root/.env.local}"; default_label="com.ericfeunekes.personal-mcp-gateway.obsidian-http"; default_health="/tmp/personal-mcp-gateway/obsidian-http-health.url" ;;
  ynab-http) env_file="${MCP_GATEWAY_YNAB_ENV_FILE:-$repo_root/.env.ynab.local}"; default_label="com.ericfeunekes.personal-mcp-gateway.ynab-http"; default_health="/tmp/personal-mcp-gateway/ynab-http-health.url" ;;
  *) fail "SERVER must be obsidian, ynab, obsidian-http, or ynab-http." ;;
esac

# shellcheck source=internal/release-config.sh
if ! source "$script_dir/internal/release-config.sh" >/dev/null 2>&1; then
  fail "local environment configuration is invalid."
fi
if [[ -f "$env_file" ]] && ! load_release_config "$env_file"; then
  fail "local environment configuration is invalid."
fi

# Health verification never needs runtime credentials. Keep them out of curl,
# launchctl, and any diagnostics those commands may emit.
unset CONTROL_PLANE_API_KEY OPENAI_API_KEY YNAB_TOKEN

if [[ "$server" == "obsidian" ]]; then
  label="${LAUNCHD_LABEL:-$default_label}"
else
  label="$default_label"
fi
# The HTTP services write a fixed health file themselves; the tunnel
# override must not point verification elsewhere.
case "$server" in
  *-http) health_url_file="$default_health" ;;
  *) health_url_file="${TUNNEL_HEALTH_URL_FILE:-$default_health}" ;;
esac
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
case "$server" in
  *-http) expected_program="$repo_root/scripts/run-${server}.sh" ;;
  *) expected_program="$repo_root/scripts/run-${server}-tunnel.sh" ;;
esac
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
