#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
env_file="$repo_root/.env.local"
make_command="${MAKE:-make}"
go_command="${GO:-go}"
uid="$(id -u)"

fail() {
  printf 'personal-mcp-gateway release: %s\n' "$1" >&2
  exit 1
}

if [[ -n "${MCP_GATEWAY_ENV_FILE:-}" && "$MCP_GATEWAY_ENV_FILE" != "$env_file" ]]; then
  fail "release uses the repo-local .env.local consumed by the LaunchAgent."
fi
unset MCP_GATEWAY_ENV_FILE

"$script_dir/release-preflight.sh"
commit="$(git -C "$repo_root" rev-parse --verify HEAD)" || fail "unable to resolve the release commit."

if [[ -f "$env_file" ]]; then
  # shellcheck disable=SC1090
  source "$env_file"
fi

# Release orchestration does not need tunnel credentials. Do not propagate them
# into tests, builds, smoke probes, or failure diagnostics.
unset CONTROL_PLANE_API_KEY OPENAI_API_KEY

candidate="${GATEWAY_CANDIDATE:-$repo_root/.build/personal-mcp-gateway}"
label="${LAUNCHD_LABEL:-com.ericfeunekes.personal-mcp-gateway.obsidian-tunnel}"

if [[ -z "${GATEWAY_BIN:-}" ]]; then
  fail "GATEWAY_BIN is required in the configured local environment file."
fi
if [[ "$GATEWAY_BIN" != /* ]]; then
  fail "GATEWAY_BIN must be an absolute path."
fi
if [[ "$candidate" != /* ]]; then
  fail "GATEWAY_CANDIDATE must be an absolute path."
fi
if [[ -z "${OBSIDIAN_ROOT:-}" || ! -d "$OBSIDIAN_ROOT" ]]; then
  fail "OBSIDIAN_ROOT is required and must name an existing directory."
fi

candidate_dir="$(dirname -- "$candidate")"
install_dir="$(dirname -- "$GATEWAY_BIN")"
mkdir -p -- "$candidate_dir" "$install_dir"
candidate_dir="$(cd -- "$candidate_dir" && pwd -P)" || fail "unable to resolve the candidate directory."
install_dir="$(cd -- "$install_dir" && pwd -P)" || fail "unable to resolve the install directory."
candidate="$candidate_dir/$(basename -- "$candidate")"
GATEWAY_BIN="$install_dir/$(basename -- "$GATEWAY_BIN")"

if [[ "$candidate" == "$GATEWAY_BIN" ]]; then
  fail "the release candidate and installed gateway must use distinct paths."
fi
if [[ -L "$candidate" || -L "$GATEWAY_BIN" ]]; then
  fail "the release candidate and installed gateway must not be symbolic links."
fi
if [[ -e "$candidate" && -e "$GATEWAY_BIN" && "$candidate" -ef "$GATEWAY_BIN" ]]; then
  fail "the release candidate and installed gateway must not reference the same file."
fi
if ! launchctl print "gui/$uid/$label" >/dev/null 2>&1; then
  fail "the tunnel LaunchAgent is not loaded; run make install-launchagent first."
fi

printf 'release: running canonical tests\n'
"$make_command" --no-print-directory -C "$repo_root" test
printf 'release: building candidate\n'
"$make_command" --no-print-directory -C "$repo_root" build

if [[ ! -x "$candidate" || -L "$candidate" ]]; then
  fail "the release candidate was not built as a regular executable."
fi
if [[ -e "$GATEWAY_BIN" && "$candidate" -ef "$GATEWAY_BIN" ]]; then
  fail "the built candidate and installed gateway reference the same file."
fi

candidate_hash="$(shasum -a 256 "$candidate" | awk '{print $1}')"
printf 'release: probing candidate over MCP stdio\n'
cd "$repo_root"
env GOCACHE="${GOCACHE:-$repo_root/.gocache}" "$go_command" run ./cmd/gateway-smoke \
  --gateway-bin "$candidate" \
  --obsidian-root "$OBSIDIAN_ROOT"

smoked_hash="$(shasum -a 256 "$candidate" | awk '{print $1}')"
if [[ "$candidate_hash" != "$smoked_hash" ]]; then
  fail "the release candidate changed during its MCP smoke probe."
fi
"$script_dir/release-preflight.sh"
current_commit="$(git -C "$repo_root" rev-parse --verify HEAD)" || fail "unable to recheck the release commit."
if [[ "$current_commit" != "$commit" ]]; then
  fail "HEAD changed during release; refusing to install the candidate."
fi

staged="$GATEWAY_BIN.next.$$"
rollback="$GATEWAY_BIN.rollback.$$"
restore_staged="$GATEWAY_BIN.restore.$$"
had_previous=0
installed_candidate=0

rollback_release() {
  local original_status="$1"
  local restored=0
  local recovery_ready=0
  trap - EXIT INT TERM HUP
  set +e

  rm -f -- "$staged" "$restore_staged"
  if (( installed_candidate == 1 )); then
    if (( had_previous == 1 )); then
      if install -m 0755 -- "$rollback" "$restore_staged" &&
        mv -f -- "$restore_staged" "$GATEWAY_BIN"; then
        restored=1
        printf 'release: previous binary restored after failed rollout\n' >&2
      else
        printf 'release: automatic binary restoration failed; rollback copy retained beside the configured target\n' >&2
      fi
    else
      if rm -f -- "$GATEWAY_BIN"; then
        restored=1
        printf 'release: removed the failed first installation\n' >&2
      else
        printf 'release: failed to remove the first failed installation\n' >&2
      fi
    fi

    if (( restored == 1 )); then
      health_url_file="${TUNNEL_HEALTH_URL_FILE:-${TMPDIR:-/tmp}/personal-mcp-gateway/tunnel-health.url}"
      if rm -f -- "$health_url_file" &&
        launchctl kickstart -k "gui/$uid/$label" >/dev/null 2>&1; then
        if (( had_previous == 0 )); then
          recovery_ready=1
        elif "$script_dir/verify-live.sh" >/dev/null 2>&1; then
          recovery_ready=1
          printf 'release: rollback runtime is live and ready\n' >&2
        fi
      fi
      if (( recovery_ready == 0 )); then
        printf 'release: prior bytes were restored but runtime recovery is unconfirmed; rollback copy retained\n' >&2
      fi
    fi
  fi

  if (( installed_candidate == 0 || recovery_ready == 1 )); then
    rm -f -- "$rollback"
  fi
  exit "$original_status"
}

trap 'rollback_release $?' EXIT
trap 'exit 130' INT TERM HUP

if [[ -e "$GATEWAY_BIN" ]]; then
  if [[ ! -x "$GATEWAY_BIN" || -L "$GATEWAY_BIN" ]]; then
    fail "the configured installed gateway is not a regular executable."
  fi
  cp -p -- "$GATEWAY_BIN" "$rollback"
  had_previous=1
fi

install -m 0755 -- "$candidate" "$staged"
staged_hash="$(shasum -a 256 "$staged" | awk '{print $1}')"
if [[ "$candidate_hash" != "$staged_hash" ]]; then
  fail "the staged binary does not match the tested release candidate."
fi
installed_candidate=1
if ! mv -f -- "$staged" "$GATEWAY_BIN"; then
  installed_candidate=0
  fail "unable to atomically install the release candidate."
fi

installed_hash="$(shasum -a 256 "$GATEWAY_BIN" | awk '{print $1}')"
if [[ "$candidate_hash" != "$installed_hash" ]]; then
  fail "the installed binary does not match the tested release candidate."
fi

health_url_file="${TUNNEL_HEALTH_URL_FILE:-${TMPDIR:-/tmp}/personal-mcp-gateway/tunnel-health.url}"
rm -f -- "$health_url_file"
printf 'release: restarting tunnel LaunchAgent\n'
launchctl kickstart -k "gui/$uid/$label"

if ! "$script_dir/verify-live.sh"; then
  fail "the restarted tunnel failed live verification."
fi

installed_candidate=0
rm -f -- "$rollback"
trap - EXIT INT TERM HUP
printf 'release complete: commit=%s sha256=%s\n' "${commit:0:12}" "${installed_hash:0:12}"
