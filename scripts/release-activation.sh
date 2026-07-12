#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
current_controller="${RELEASE_ACTIVATION_CANDIDATE:-$repo_root/.build/release-activation}"
readonly output_limit=65536

fail_literal() {
  printf '%s\n' 'error=authority_missing message=the release controller is unavailable' >&2
  exit 1
}

passwd_home() {
  local username record home
  username="$(/usr/bin/id -un)" || return 1
  record="$(/usr/bin/dscl . -read "/Users/$username" NFSHomeDirectory 2>/dev/null)" || return 1
  home="${record#NFSHomeDirectory: }"
  if [[ -z "$home" || "$home" == "$record" || "$home" != /* ]]; then
    return 1
  fi
  printf '%s\n' "$home"
}

home="$(passwd_home)" || fail_literal
state_root="$home/Library/Application Support/personal-mcp-gateway/release/obsidian"
active="$state_root/active"
authority="$active/authority"
command="${1:-status}"

selected=""
selection=""
select_controller() {
  selected=""
  selection=""
  if [[ -e "$active" || -L "$active" ]]; then
    if [[ ! -d "$active" || -L "$active" || ! -f "$authority" || -L "$authority" || ! -x "$authority" ]]; then
      return 1
    fi
    selected="$authority"
    selection="active"
    return 0
  fi

  case "$command" in
    status)
      selection="clear-status"
      return 0
      ;;
    resume-if-active)
      selection="clear-resume"
      return 0
      ;;
  esac
  if [[ ! -f "$current_controller" || -L "$current_controller" || ! -x "$current_controller" ]]; then
    return 1
  fi
  selected="$current_controller"
  selection="current"
}

umask 077
channel_dir="$(mktemp -d "${TMPDIR:-/tmp}/release-activation.XXXXXX" 2>/dev/null)" || fail_literal
stdout_file="$channel_dir/stdout"
stderr_file="$channel_dir/stderr"
cleanup() {
  rm -f -- "$stdout_file" "$stderr_file" 2>/dev/null || true
  rmdir -- "$channel_dir" 2>/dev/null || true
}
trap cleanup EXIT HUP INT TERM

child_args=("$@")
if [[ "$command" == "resume-if-active" ]]; then
  child_args=(release)
fi

attempt=0
while (( attempt < 2 )); do
  if ! select_controller; then
    fail_literal
  fi
  case "$selection" in
    clear-status)
      printf '%s\n' 'state=clear'
      exit 0
      ;;
    clear-resume)
      exit 3
      ;;
  esac

  : >"$stdout_file"
  : >"$stderr_file"
  child_status=0
  (
    ulimit -f 64
    exec "$selected" "${child_args[@]}"
  ) >"$stdout_file" 2>"$stderr_file" || child_status=$?

  stdout_size="$(wc -c <"$stdout_file" 2>/dev/null)" || fail_literal
  stderr_size="$(wc -c <"$stderr_file" 2>/dev/null)" || fail_literal
  if (( stdout_size > output_limit || stderr_size > output_limit )); then
    fail_literal
  fi

  start_failure=0
  if (( child_status == 126 || child_status == 127 )); then
    start_failure=1
  fi
  first_error=""
  IFS= read -r first_error <"$stderr_file" || true
  authority_retry=0
  if (( child_status == 1 )) && [[ ! -s "$stdout_file" ]] &&
    [[ "$first_error" == 'error=authority_mismatch message=release controller identity does not match' ]]; then
    authority_retry=1
  fi

  if (( start_failure == 1 || authority_retry == 1 )); then
    if (( attempt == 0 )); then
      attempt=1
      continue
    fi
    fail_literal
  fi
  if (( child_status < 0 || child_status > 2 )); then
    fail_literal
  fi

  command cat -- "$stdout_file" || fail_literal
  command cat -- "$stderr_file" >&2 || fail_literal
  exit "$child_status"
done

fail_literal
