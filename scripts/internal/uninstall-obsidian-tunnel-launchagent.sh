#!/usr/bin/env bash
set -euo pipefail

home="${1:?}"
uid="${2:?}"
label="${3:?}"

valid_label() {
  local candidate="$1"
  ((${#candidate} <= 255)) &&
    [[ "$candidate" =~ ^[A-Za-z0-9]([A-Za-z0-9_-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9_-]*[A-Za-z0-9])?)*$ ]]
}

[[ "$home" = /* && "$uid" =~ ^[0-9]+$ && "$home" != *$'\n'* && "$home" != *$'\r'* ]] || exit 2
valid_label "$label" || exit 2
[[ -d "$home" && ! -L "$home" ]] || exit 2
home="$(cd -P -- "$home" && pwd)"
launch_agents_dir="$home/Library/LaunchAgents"
plist_path="$launch_agents_dir/$label.plist"
[[ ! -L "$home/Library" && ! -L "$launch_agents_dir" && ! -L "$plist_path" ]] || exit 2
[[ "$(dirname -- "$plist_path")" == "$launch_agents_dir" && "$(basename -- "$plist_path")" == "$label.plist" ]] || exit 2
if [[ -d "$launch_agents_dir" ]]; then
  [[ "$(cd -P -- "$launch_agents_dir" && pwd)" == "$launch_agents_dir" ]] || exit 2
fi

if [[ -e "$plist_path" ]]; then
  [[ -f "$plist_path" && ! -L "$plist_path" ]] || exit 2
  /usr/bin/plutil -lint "$plist_path" >/dev/null
  [[ "$(/usr/bin/plutil -extract Label raw "$plist_path")" == "$label" ]] || exit 2
fi

if launchctl print "gui/$uid/$label" >/dev/null 2>&1; then
  launchctl bootout "gui/$uid" "$plist_path"
fi
if launchctl print "gui/$uid/$label" >/dev/null 2>&1; then
  exit 1
fi
rm -f -- "$plist_path"
