#!/usr/bin/env bash
set -euo pipefail

home="${1:?}"
uid="${2:?}"
label="${3:?}"
runner_path="${4:?}"
stdout_path="${5:?}"
stderr_path="${6:?}"
repo_root="${7:?}"

valid_label() {
  local candidate="$1"
  ((${#candidate} <= 255)) &&
    [[ "$candidate" =~ ^[A-Za-z0-9]([A-Za-z0-9_-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9_-]*[A-Za-z0-9])?)*$ ]]
}

xml_escape() {
  local escaped="$1"
  escaped=${escaped//&/\&amp;}
  escaped=${escaped//</\&lt;}
  escaped=${escaped//>/\&gt;}
  escaped=${escaped//\"/\&quot;}
  escaped=${escaped//\'/\&apos;}
  printf '%s' "$escaped"
}

reject_xml_controls() {
  [[ "$1" != *$'\n'* && "$1" != *$'\r'* ]]
}

[[ "$home" = /* && "$uid" =~ ^[0-9]+$ && "$runner_path" = /* && "$stdout_path" = /* && "$stderr_path" = /* && "$repo_root" = /* ]] || exit 2
valid_label "$label" || exit 2
reject_xml_controls "$home" && reject_xml_controls "$label" &&
  reject_xml_controls "$runner_path" && reject_xml_controls "$stdout_path" && reject_xml_controls "$stderr_path" &&
  reject_xml_controls "$repo_root" || exit 2
[[ -d "$home" && ! -L "$home" ]] || exit 2
[[ -d "$repo_root" && ! -L "$repo_root" ]] || exit 2

home="$(cd -P -- "$home" && pwd)"
repo_root="$(cd -P -- "$repo_root" && pwd)"
library_dir="$home/Library"
launch_agents_dir="$library_dir/LaunchAgents"
plist_path="$launch_agents_dir/$label.plist"

[[ ! -L "$library_dir" && ! -L "$launch_agents_dir" && ! -L "$plist_path" ]] || exit 2
[[ "$(dirname -- "$plist_path")" == "$launch_agents_dir" && "$(basename -- "$plist_path")" == "$label.plist" ]] || exit 2
[[ -f "$runner_path" && ! -L "$runner_path" ]] || exit 2

mkdir -p -- "$launch_agents_dir"
[[ ! -L "$launch_agents_dir" ]] || exit 2
[[ "$(cd -P -- "$launch_agents_dir" && pwd)" == "$launch_agents_dir" ]] || exit 2

# stdout/stderr paths are caller-supplied arguments, not built from the
# already-canonicalized $home the way launch_agents_dir is, so (unlike that
# directory) there is no already-resolved baseline to re-check a leaf-only
# symlink check against. Require both to canonicalize under the
# canonicalized, trusted log root instead: this rejects a log path outside
# personal-mcp-gateway's own log directory even if some component of it is a
# symlink that a leaf-only check would miss.
log_root="$home/Library/Logs/personal-mcp-gateway"
mkdir -p -- "$log_root"
[[ -d "$log_root" && ! -L "$log_root" ]] || exit 2
canonical_log_root="$(cd -P -- "$log_root" && pwd)"
for log_path in "$stdout_path" "$stderr_path"; do
  log_dir="$(dirname -- "$log_path")"
  mkdir -p -- "$log_dir"
  [[ -d "$log_dir" && ! -L "$log_dir" ]] || exit 2
  [[ "$(cd -P -- "$log_dir" && pwd)" == "$canonical_log_root" ]] || exit 2
done

label_xml="$(xml_escape "$label")"
runner_xml="$(xml_escape "$runner_path")"
repo_root_xml="$(xml_escape "$repo_root")"
stdout_xml="$(xml_escape "$stdout_path")"
stderr_xml="$(xml_escape "$stderr_path")"

tmp_plist="$(mktemp "$plist_path.XXXXXX")"
trap 'rm -f -- "$tmp_plist"' EXIT
cat >"$tmp_plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$label_xml</string>
  <key>ProgramArguments</key>
  <array>
    <string>$runner_xml</string>
  </array>
  <key>WorkingDirectory</key>
  <string>$repo_root_xml</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>30</integer>
  <key>ProcessType</key>
  <string>Background</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    <key>TMPDIR</key>
    <string>/tmp</string>
  </dict>
  <key>StandardOutPath</key>
  <string>$stdout_xml</string>
  <key>StandardErrorPath</key>
  <string>$stderr_xml</string>
</dict>
</plist>
PLIST

chmod 644 "$tmp_plist"
/usr/bin/plutil -lint "$tmp_plist" >/dev/null
[[ "$(/usr/bin/plutil -extract Label raw "$tmp_plist")" == "$label" ]]
[[ "$(/usr/bin/plutil -extract ProgramArguments.0 raw "$tmp_plist")" == "$runner_path" ]]
[[ "$(/usr/bin/plutil -extract WorkingDirectory raw "$tmp_plist")" == "$repo_root" ]]
[[ "$(/usr/bin/plutil -extract StandardOutPath raw "$tmp_plist")" == "$stdout_path" ]]
[[ "$(/usr/bin/plutil -extract StandardErrorPath raw "$tmp_plist")" == "$stderr_path" ]]
mv "$tmp_plist" "$plist_path"
trap - EXIT
/usr/bin/plutil -lint "$plist_path" >/dev/null
[[ "$(/usr/bin/plutil -extract Label raw "$plist_path")" == "$label" ]]
[[ "$(/usr/bin/plutil -extract ProgramArguments.0 raw "$plist_path")" == "$runner_path" ]]
[[ "$(/usr/bin/plutil -extract WorkingDirectory raw "$plist_path")" == "$repo_root" ]]
[[ "$(/usr/bin/plutil -extract StandardOutPath raw "$plist_path")" == "$stdout_path" ]]
[[ "$(/usr/bin/plutil -extract StandardErrorPath raw "$plist_path")" == "$stderr_path" ]]

if launchctl print "gui/$uid/$label" >/dev/null 2>&1; then
  launchctl bootout "gui/$uid" "$plist_path" >/dev/null 2>&1 || true
fi

# Install must never unload a job on failure: once bootstrap below succeeds,
# nothing in the rest of this script unloads it again, even if a later step
# (kickstart or the final print) fails. Readiness is confirmed separately
# with `make verify-live`, not by this adapter rolling itself back.
launchctl bootstrap "gui/$uid" "$plist_path"
# RunAtLoad already started the job. A killing kickstart here would land inside
# launchd's ThrottleInterval and stall the respawn past the controller's child
# deadline, so only ensure it is running.
launchctl kickstart "gui/$uid/$label"
launchctl print "gui/$uid/$label"
