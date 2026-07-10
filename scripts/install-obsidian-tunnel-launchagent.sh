#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"

label="com.ericfeunekes.personal-mcp-gateway.obsidian-tunnel"
plist_path="$HOME/Library/LaunchAgents/$label.plist"
log_dir="$HOME/Library/Logs/personal-mcp-gateway"
uid="$(id -u)"

mkdir -p -- "$HOME/Library/LaunchAgents" "$log_dir"

tmp_plist="$(mktemp "$plist_path.XXXXXX")"
cat >"$tmp_plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$label</string>
  <key>ProgramArguments</key>
  <array>
    <string>$repo_root/scripts/run-obsidian-tunnel.sh</string>
  </array>
  <key>WorkingDirectory</key>
  <string>$repo_root</string>
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
  <string>$log_dir/obsidian-tunnel.out.log</string>
  <key>StandardErrorPath</key>
  <string>$log_dir/obsidian-tunnel.err.log</string>
</dict>
</plist>
PLIST

chmod 644 "$tmp_plist"
mv "$tmp_plist" "$plist_path"

if launchctl print "gui/$uid/$label" >/dev/null 2>&1; then
  launchctl bootout "gui/$uid" "$plist_path" >/dev/null 2>&1 || true
fi

launchctl bootstrap "gui/$uid" "$plist_path"
launchctl kickstart -k "gui/$uid/$label"
launchctl print "gui/$uid/$label"
