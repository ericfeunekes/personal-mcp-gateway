#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
current_controller="${RELEASE_ACTIVATION_CANDIDATE:-$repo_root/.build/release-activation}"
readonly output_limit=65536
readonly capture_limit=$((output_limit + 1))
readonly controller_timeout_seconds=720

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
stdout_pipe="$channel_dir/stdout.pipe"
stderr_pipe="$channel_dir/stderr.pipe"
stdout_done="$channel_dir/stdout.done"
stderr_done="$channel_dir/stderr.done"
watchdog_pipe="$channel_dir/watchdog.pipe"
watchdog_done="$channel_dir/watchdog.done"
stdout_capture_pid=""
stderr_capture_pid=""
controller_pid=""
watchdog_pid=""

stop_controller_processes() {
  stop_controller_watchdog
  if [[ -n "$controller_pid" ]]; then
    kill -KILL -- "-$controller_pid" 2>/dev/null || true
    wait "$controller_pid" 2>/dev/null || true
    controller_pid=""
  fi
}

stop_capture_processes() {
  if [[ -n "$stdout_capture_pid" ]]; then
    kill -KILL -- "-$stdout_capture_pid" 2>/dev/null || true
    wait "$stdout_capture_pid" 2>/dev/null || true
    stdout_capture_pid=""
  fi
  if [[ -n "$stderr_capture_pid" ]]; then
    kill -KILL -- "-$stderr_capture_pid" 2>/dev/null || true
    wait "$stderr_capture_pid" 2>/dev/null || true
    stderr_capture_pid=""
  fi
}

cleanup() {
  stop_controller_processes
  stop_capture_processes
  /bin/rm -f -- "$stdout_file" "$stderr_file" "$stdout_pipe" "$stderr_pipe" "$stdout_done" "$stderr_done" "$watchdog_pipe" "$watchdog_done" 2>/dev/null || true
  /bin/rmdir -- "$channel_dir" 2>/dev/null || true
}
trap cleanup EXIT HUP INT TERM

if ! /usr/bin/mkfifo "$stdout_pipe" "$stderr_pipe" 2>/dev/null; then
  fail_literal
fi

capture_channel() {
  local pipe="$1"
  local output="$2"
  local done="$3"
  (
    exec 3<"$pipe"
    /usr/bin/head -c "$capture_limit" <&3 >"$output"
    /bin/cat <&3 >/dev/null
    : >"$done"
  )
}

start_capture_processes() {
  /bin/rm -f -- "$stdout_done" "$stderr_done" 2>/dev/null || return 1
  : >"$stdout_file" || return 1
  : >"$stderr_file" || return 1
  set -m
  capture_channel "$stdout_pipe" "$stdout_file" "$stdout_done" 2>/dev/null &
  stdout_capture_pid=$!
  capture_channel "$stderr_pipe" "$stderr_file" "$stderr_done" 2>/dev/null &
  stderr_capture_pid=$!
  set +m
}

wait_for_capture_processes() {
  local iteration
  for (( iteration = 0; iteration < 100; iteration++ )); do
    if [[ -e "$stdout_done" && -e "$stderr_done" ]]; then
      wait "$stdout_capture_pid" || return 1
      stdout_capture_pid=""
      wait "$stderr_capture_pid" || return 1
      stderr_capture_pid=""
      return 0
    fi
    if { ! kill -0 "$stdout_capture_pid" 2>/dev/null && [[ ! -e "$stdout_done" ]]; } ||
      { ! kill -0 "$stderr_capture_pid" 2>/dev/null && [[ ! -e "$stderr_done" ]]; }; then
      stop_capture_processes
      return 1
    fi
    /bin/sleep 0.01
  done
  stop_capture_processes
  return 1
}

start_controller_watchdog() {
  local watched_pid="$1"
  /bin/rm -f -- "$watchdog_pipe" "$watchdog_done" 2>/dev/null || return 1
  /usr/bin/mkfifo "$watchdog_pipe" 2>/dev/null || return 1
  exec 9<>"$watchdog_pipe" || return 1
  (
    if ! IFS= read -r -t "$controller_timeout_seconds" <&9; then
      kill -TERM -- "-$watched_pid" 2>/dev/null || true
      /bin/sleep 1
      kill -KILL -- "-$watched_pid" 2>/dev/null || true
    fi
    : >"$watchdog_done"
  ) >/dev/null 2>&1 &
  watchdog_pid=$!
}

stop_controller_watchdog() {
  if [[ -n "$watchdog_pid" ]]; then
    if [[ ! -e "$watchdog_done" ]]; then
      printf '\n' >&9 2>/dev/null || true
    fi
    wait "$watchdog_pid" 2>/dev/null || true
    watchdog_pid=""
    exec 9>&-
    /bin/rm -f -- "$watchdog_pipe" "$watchdog_done" 2>/dev/null || true
  fi
}

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

  if ! start_capture_processes; then
    fail_literal
  fi
  child_status=0
  set -m
  "$selected" "${child_args[@]}" >"$stdout_pipe" 2>"$stderr_pipe" &
  controller_pid=$!
  set +m
  if ! start_controller_watchdog "$controller_pid"; then
    fail_literal
  fi
  wait "$controller_pid" || child_status=$?
  stop_controller_watchdog

  if ! wait_for_capture_processes; then
    fail_literal
  fi
  # The leader has exited, but a misbehaving controller may have left
  # descendants in its process group. Capture completion proves their channel
  # descriptors are closed; kill any survivors before relay or retry.
  kill -KILL -- "-$controller_pid" 2>/dev/null || true
  controller_pid=""

  stdout_size="$(/usr/bin/wc -c <"$stdout_file" 2>/dev/null)" || fail_literal
  stderr_size="$(/usr/bin/wc -c <"$stderr_file" 2>/dev/null)" || fail_literal
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

  /bin/cat -- "$stdout_file" || fail_literal
  /bin/cat -- "$stderr_file" >&2 || fail_literal
  exit "$child_status"
done

fail_literal
