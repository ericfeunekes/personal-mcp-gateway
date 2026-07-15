#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
env_file="$repo_root/.env.local"
make_command="${MAKE:-make}"
go_command="${GO:-go}"
uid="$(id -u)"

fail() {
  printf 'error=%s message=%s\n' "$1" "$2" >&2
  exit 1
}

# shellcheck source=internal/release-config.sh
if ! source "$script_dir/internal/release-config.sh" >/dev/null 2>&1; then
  fail release_config 'release configuration is invalid'
fi

# Release control and host verification never need model or tunnel credentials.
unset CONTROL_PLANE_API_KEY OPENAI_API_KEY

if "$script_dir/release-activation.sh" resume-if-active; then
  exit 0
else
  resume_status=$?
  if (( resume_status != 3 )); then
    exit "$resume_status"
  fi
fi

if [[ -n "${MCP_GATEWAY_ENV_FILE:-}" && "$MCP_GATEWAY_ENV_FILE" != "$env_file" ]]; then
  fail release_config 'release configuration is invalid'
fi
unset MCP_GATEWAY_ENV_FILE

if ! "$script_dir/release-preflight.sh" >/dev/null 2>&1; then
  fail release_preflight_failed 'release preflight failed'
fi
commit="$(git -C "$repo_root" rev-parse --verify HEAD 2>/dev/null)" || fail release_preflight_failed 'release preflight failed'

if [[ -f "$env_file" ]]; then
  load_release_config "$env_file" || fail release_config 'release configuration is invalid'
fi
unset CONTROL_PLANE_API_KEY OPENAI_API_KEY

candidate="${GATEWAY_CANDIDATE:-$repo_root/.build/personal-mcp-gateway}"
controller="${RELEASE_ACTIVATION_CANDIDATE:-$repo_root/.build/release-activation}"
label="${LAUNCHD_LABEL:-com.ericfeunekes.personal-mcp-gateway.obsidian-tunnel}"

if [[ -z "${GATEWAY_BIN:-}" ]]; then
  fail release_config 'release configuration is invalid'
fi
if [[ "$GATEWAY_BIN" != /* ]]; then
  fail release_config 'release configuration is invalid'
fi
if [[ "$candidate" != /* ]]; then
  fail release_config 'release configuration is invalid'
fi
if [[ "$controller" != /* ]]; then
  fail release_config 'release configuration is invalid'
fi
if [[ -z "${OBSIDIAN_ROOT:-}" || ! -d "$OBSIDIAN_ROOT" ]]; then
  fail release_config 'release configuration is invalid'
fi

candidate_dir="$(dirname -- "$candidate")"
install_dir="$(dirname -- "$GATEWAY_BIN")"
mkdir -p -- "$candidate_dir" "$install_dir" 2>/dev/null || fail release_config 'release configuration is invalid'
candidate_dir="$(cd -- "$candidate_dir" 2>/dev/null && pwd -P)" || fail release_config 'release configuration is invalid'
install_dir="$(cd -- "$install_dir" 2>/dev/null && pwd -P)" || fail release_config 'release configuration is invalid'
candidate="$candidate_dir/$(basename -- "$candidate")"
GATEWAY_BIN="$install_dir/$(basename -- "$GATEWAY_BIN")"

if [[ "$candidate" == "$GATEWAY_BIN" ]]; then
  fail release_config 'release configuration is invalid'
fi
if [[ -L "$candidate" || -L "$GATEWAY_BIN" ]]; then
  fail release_config 'release configuration is invalid'
fi
if [[ -e "$candidate" && -e "$GATEWAY_BIN" && "$candidate" -ef "$GATEWAY_BIN" ]]; then
  fail release_config 'release configuration is invalid'
fi
if ! launchctl print "gui/$uid/$label" >/dev/null 2>&1; then
  fail runtime_unavailable 'supervised runtime is unavailable'
fi

if ! "$make_command" --no-print-directory -C "$repo_root" test >/dev/null 2>&1; then
  fail release_test_failed 'release tests failed'
fi
if ! "$make_command" --no-print-directory -C "$repo_root" build >/dev/null 2>&1; then
  fail release_build_failed 'release build failed'
fi
if ! "$make_command" --no-print-directory -C "$repo_root" build-release-controller >/dev/null 2>&1; then
  fail release_build_failed 'release build failed'
fi

if [[ ! -x "$candidate" || -L "$candidate" ]]; then
  fail release_build_failed 'release build failed'
fi
if [[ ! -x "$controller" || -L "$controller" ]]; then
  fail release_build_failed 'release build failed'
fi
if [[ -e "$GATEWAY_BIN" && "$candidate" -ef "$GATEWAY_BIN" ]]; then
  fail release_config 'release configuration is invalid'
fi

candidate_hash="$(shasum -a 256 "$candidate" 2>/dev/null | awk '{print $1}')" || fail release_build_failed 'release build failed'
cd "$repo_root" 2>/dev/null || fail release_config 'release configuration is invalid'
if ! env GOCACHE="${GOCACHE:-$repo_root/.gocache}" "$go_command" run ./cmd/gateway-smoke \
  --gateway-bin "$candidate" \
  --obsidian-root "$OBSIDIAN_ROOT" >/dev/null 2>&1; then
  fail release_smoke_failed 'release candidate smoke failed'
fi
if ! env GOCACHE="${GOCACHE:-$repo_root/.gocache}" "$go_command" run ./cmd/gateway-smoke \
  --gateway-bin "$candidate" \
  --obsidian-root "$OBSIDIAN_ROOT" \
  --performance-json >/dev/null 2>&1; then
  fail release_smoke_failed 'release candidate performance smoke failed'
fi
if ! env GOCACHE="${GOCACHE:-$repo_root/.gocache}" "$go_command" run ./cmd/gateway-smoke \
  --gateway-bin "$candidate" \
  --obsidian-root "$OBSIDIAN_ROOT" \
  --resource-json >/dev/null 2>&1; then
  fail release_smoke_failed 'release candidate resource smoke failed'
fi

smoked_hash="$(shasum -a 256 "$candidate" 2>/dev/null | awk '{print $1}')" || fail release_smoke_failed 'release candidate smoke failed'
if [[ "$candidate_hash" != "$smoked_hash" ]]; then
  fail release_changed 'release inputs changed during validation'
fi
if ! "$script_dir/release-preflight.sh" >/dev/null 2>&1; then
  fail release_preflight_failed 'release preflight failed'
fi
current_commit="$(git -C "$repo_root" rev-parse --verify HEAD 2>/dev/null)" || fail release_preflight_failed 'release preflight failed'
if [[ "$current_commit" != "$commit" ]]; then
  fail release_changed 'release inputs changed during validation'
fi
health_url_file="${TUNNEL_HEALTH_URL_FILE:-/tmp/personal-mcp-gateway/tunnel-health.url}"
ready_timeout="${RELEASE_READY_TIMEOUT_SECONDS:-45}"
ready_poll="${RELEASE_READY_POLL_SECONDS:-1}"
if ! [[ "$ready_timeout" =~ ^[1-9][0-9]*$ ]]; then
  fail release_config 'release configuration is invalid'
fi
if ! [[ "$ready_poll" =~ ^([0-9]+)(\.([0-9]{1,3}))?$ ]]; then
  fail release_config 'release configuration is invalid'
fi
poll_whole="${BASH_REMATCH[1]}"
poll_fraction="${BASH_REMATCH[3]:-0}000"
poll_fraction="${poll_fraction:0:3}"
ready_poll_ms=$((10#$poll_whole * 1000 + 10#$poll_fraction))
if (( ready_poll_ms <= 0 )); then
  fail release_config 'release configuration is invalid'
fi

exec "$script_dir/release-activation.sh" release \
  --commit "$commit" \
  --candidate "$candidate" \
  --authority "$controller" \
  --target "$GATEWAY_BIN" \
  --label "$label" \
  --repo-root "$repo_root" \
  --environment "$env_file" \
  --health-url-file "$health_url_file" \
  --ready-timeout-seconds "$ready_timeout" \
  --ready-poll-milliseconds "$ready_poll_ms"
