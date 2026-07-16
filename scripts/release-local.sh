#!/usr/bin/env bash
set -euo pipefail
umask 077

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

dependency_digest() {
  local go_mod_hash go_sum_hash
  go_mod_hash="$(shasum -a 256 "$repo_root/go.mod" 2>/dev/null | awk '{print $1}')" || return 1
  go_sum_hash="$(shasum -a 256 "$repo_root/go.sum" 2>/dev/null | awk '{print $1}')" || return 1
  printf 'go.mod=%s\ngo.sum=%s\n' "$go_mod_hash" "$go_sum_hash" | shasum -a 256 | awk '{print $1}'
}

report_dir=""
cleanup_reports() {
  if [[ -n "$report_dir" ]]; then
    rm -rf -- "$report_dir"
  fi
}
trap cleanup_reports EXIT

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
dependency_hash="$(dependency_digest)" || fail release_build_failed 'release dependency identity failed'
report_dir="$(mktemp -d "/tmp/personal-mcp-gateway-release-reports.XXXXXX")" || fail release_smoke_failed 'release report setup failed'
chmod 700 "$report_dir" 2>/dev/null || fail release_smoke_failed 'release report setup failed'
smoke_candidate="$report_dir/candidate"
if ! cp -- "$candidate" "$smoke_candidate" 2>/dev/null || ! chmod 700 "$smoke_candidate" 2>/dev/null; then
  fail release_smoke_failed 'release candidate snapshot failed'
fi
smoke_candidate_hash="$(shasum -a 256 "$smoke_candidate" 2>/dev/null | awk '{print $1}')" || fail release_smoke_failed 'release candidate snapshot failed'
if [[ "$smoke_candidate_hash" != "$candidate_hash" || -L "$smoke_candidate" || ! -x "$smoke_candidate" ]]; then
  fail release_smoke_failed 'release candidate snapshot failed'
fi
functional_report="$report_dir/functional.json"
performance_report="$report_dir/performance.json"
resource_report="$report_dir/resource.json"
cd "$repo_root" 2>/dev/null || fail release_config 'release configuration is invalid'

if ! (ulimit -f 2048; env GOCACHE="${GOCACHE:-$repo_root/.gocache}" "$go_command" run ./cmd/gateway-smoke \
  --gateway-bin "$smoke_candidate" --obsidian-root "$OBSIDIAN_ROOT" \
  --repo-root "$repo_root" --candidate-commit "$commit" \
  --candidate-sha256 "$candidate_hash" --dependency-sha256 "$dependency_hash" \
  --report-json >"$functional_report" 2>/dev/null); then
  fail release_smoke_failed 'release candidate smoke failed'
fi
if [[ ! -s "$functional_report" || "$(wc -c <"$functional_report")" -gt 1048576 ]]; then
  fail release_smoke_failed 'release candidate smoke report is invalid'
fi
if ! (ulimit -f 2048; env GOCACHE="${GOCACHE:-$repo_root/.gocache}" "$go_command" run ./cmd/gateway-smoke \
  --gateway-bin "$smoke_candidate" --obsidian-root "$OBSIDIAN_ROOT" \
  --repo-root "$repo_root" --candidate-commit "$commit" \
  --candidate-sha256 "$candidate_hash" --dependency-sha256 "$dependency_hash" \
  --performance-json >"$performance_report" 2>/dev/null); then
  fail release_smoke_failed 'release candidate performance smoke failed'
fi
if [[ ! -s "$performance_report" || "$(wc -c <"$performance_report")" -gt 1048576 ]]; then
  fail release_smoke_failed 'release candidate performance report is invalid'
fi
if ! (ulimit -f 2048; env GOCACHE="${GOCACHE:-$repo_root/.gocache}" "$go_command" run ./cmd/gateway-smoke \
  --gateway-bin "$smoke_candidate" --obsidian-root "$OBSIDIAN_ROOT" \
  --repo-root "$repo_root" --candidate-commit "$commit" \
  --candidate-sha256 "$candidate_hash" --dependency-sha256 "$dependency_hash" \
  --resource-json >"$resource_report" 2>/dev/null); then
  fail release_smoke_failed 'release candidate resource smoke failed'
fi
if [[ ! -s "$resource_report" || "$(wc -c <"$resource_report")" -gt 1048576 ]]; then
  fail release_smoke_failed 'release candidate resource report is invalid'
fi

smoked_snapshot_hash="$(shasum -a 256 "$smoke_candidate" 2>/dev/null | awk '{print $1}')" || fail release_smoke_failed 'release candidate smoke failed'
if [[ "$candidate_hash" != "$smoked_snapshot_hash" ]]; then
  fail release_changed 'release inputs changed during validation'
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
current_dependency_hash="$(dependency_digest)" || fail release_changed 'release inputs changed during validation'
if [[ "$current_dependency_hash" != "$dependency_hash" ]]; then
  fail release_changed 'release inputs changed during validation'
fi
if ! env GOCACHE="${GOCACHE:-$repo_root/.gocache}" "$go_command" run ./cmd/gateway-smoke \
  --gateway-bin "$smoke_candidate" --repo-root "$repo_root" \
  --candidate-commit "$commit" --candidate-sha256 "$candidate_hash" \
  --dependency-sha256 "$dependency_hash" --validate-report-set \
  "$functional_report" "$performance_report" "$resource_report" >/dev/null 2>&1; then
  fail release_smoke_failed 'release candidate report set is invalid'
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

cleanup_reports
report_dir=""
exec "$script_dir/release-activation.sh" release \
  --commit "$commit" \
  --candidate-sha256 "$candidate_hash" \
  --dependency-sha256 "$dependency_hash" \
  --candidate "$candidate" \
  --authority "$controller" \
  --target "$GATEWAY_BIN" \
  --label "$label" \
  --repo-root "$repo_root" \
  --environment "$env_file" \
  --health-url-file "$health_url_file" \
  --ready-timeout-seconds "$ready_timeout" \
  --ready-poll-milliseconds "$ready_poll_ms"
