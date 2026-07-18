#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
make_command="${MAKE:-make}"

fail() {
  printf 'error=%s message=%s\n' "$1" "$2" >&2
  exit 1
}

branch="$(git -C "$repo_root" branch --show-current 2>/dev/null)" || fail update_preflight_failed 'update preflight failed'
if [[ "$branch" != "main" ]]; then
  fail update_preflight_failed 'update preflight failed'
fi
status="$(git -C "$repo_root" status --porcelain --untracked-files=all 2>/dev/null)" || fail update_preflight_failed 'update preflight failed'
if [[ -n "$status" ]]; then
  fail update_preflight_failed 'update preflight failed'
fi
expected_head="$(git -C "$repo_root" rev-parse HEAD 2>/dev/null)" || fail update_preflight_failed 'update preflight failed'

if ! git -C "$repo_root" fetch origin main >/dev/null 2>&1; then
  fail update_fetch_failed 'source fetch failed'
fi
remote_oid="$(git -C "$repo_root" rev-parse --verify 'FETCH_HEAD^{commit}' 2>/dev/null)" || fail update_fetch_failed 'source fetch failed'
if ! [[ "$remote_oid" =~ ^([0-9a-f]{40}|[0-9a-f]{64})$ ]]; then
  fail update_fetch_failed 'source fetch failed'
fi
if ! "$make_command" --no-print-directory -C "$repo_root" build-release-controller >/dev/null 2>&1; then
  fail release_build_failed 'release build failed'
fi
update_status=0
"$script_dir/release-activation.sh" update-after-fetch \
  --repo "$repo_root" \
  --expected-head "$expected_head" \
  --expected-remote-oid "$remote_oid" >/dev/null || update_status=$?
if (( update_status != 0 )); then
  exit "$update_status"
fi
exec "$script_dir/release-local.sh"
