#!/usr/bin/env bash

# This trusted helper parses the ignored local environment as bounded data. It
# intentionally does not enable shell options or emit output so callers retain
# control of their exact public failure grammar.

release_config_expand_home_prefix() {
  local value="$1"
  case "$value" in
    '$HOME') printf '%s' "$HOME" ;;
    '$HOME/'*) printf '%s/%s' "$HOME" "${value#\$HOME/}" ;;
    '${HOME}') printf '%s' "$HOME" ;;
    '${HOME}/'*) printf '%s/%s' "$HOME" "${value#\$\{HOME\}/}" ;;
    *'$'*) return 1 ;;
    *) printf '%s' "$value" ;;
  esac
}

load_release_config() {
  local path="$1" size line key raw value inner
  local seen_keys=$'\n'
  [[ -f "$path" && ! -L "$path" ]] || return 1
  size="$(/usr/bin/wc -c <"$path" 2>/dev/null)" || return 1
  size="${size//[[:space:]]/}"
  [[ "$size" =~ ^[0-9]+$ ]] || return 1
  (( size <= 65536 )) || return 1

  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    (( ${#line} <= 4096 )) || return 1
    [[ -z "$line" || "$line" == \#* ]] && continue
    [[ "$line" =~ ^([A-Z][A-Z0-9_]*)=(.*)$ ]] || return 1
    key="${BASH_REMATCH[1]}"
    raw="${BASH_REMATCH[2]}"
    case "$key" in
      CONTROL_PLANE_TUNNEL_ID|CONTROL_PLANE_API_KEY|OBSIDIAN_ROOT|GATEWAY_BIN|MCP_GATEWAY_STATE_DIR|MCP_GATEWAY_TELEMETRY_DB|TUNNEL_HEALTH_LISTEN_ADDR|TUNNEL_LOG_FORMAT|TUNNEL_HEALTH_URL_FILE|RELEASE_READY_TIMEOUT_SECONDS|RELEASE_READY_POLL_SECONDS) ;;
      *) return 1 ;;
    esac
    case "$seen_keys" in
      *$'\n'"$key"$'\n'*) return 1 ;;
    esac
    seen_keys="${seen_keys}${key}"$'\n'

    if [[ "$raw" == \"* ]]; then
      [[ ${#raw} -ge 2 && "$raw" == *\" ]] || return 1
      inner="${raw:1:${#raw}-2}"
      [[ "$inner" != *'`'* && "$inner" != *'\'* && "$inner" != *'"'* ]] || return 1
      value="$(release_config_expand_home_prefix "$inner")" || return 1
    elif [[ "$raw" == \'* ]]; then
      [[ ${#raw} -ge 2 && "$raw" == *\' ]] || return 1
      inner="${raw:1:${#raw}-2}"
      [[ "$inner" != *"'"* ]] || return 1
      value="$inner"
    else
      [[ "$raw" != *[[:space:]\#\"\'\`\\]* ]] || return 1
      value="$(release_config_expand_home_prefix "$raw")" || return 1
    fi
    printf -v "$key" '%s' "$value"
    export "$key"
  done <"$path"
}
