#!/usr/bin/env bash
set -euo pipefail

label="com.ericfeunekes.personal-mcp-gateway.obsidian-tunnel"
plist_path="$HOME/Library/LaunchAgents/$label.plist"
uid="$(id -u)"

launchctl bootout "gui/$uid" "$plist_path" >/dev/null 2>&1 || true
rm -f -- "$plist_path"

printf 'Uninstalled %s\n' "$label"
