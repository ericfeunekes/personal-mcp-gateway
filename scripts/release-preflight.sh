#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"

fail() {
  printf 'personal-mcp-gateway release: %s\n' "$1" >&2
  exit 1
}

status="$(git -C "$repo_root" status --porcelain --untracked-files=all)" || fail "unable to inspect the working tree."
if [[ -n "$status" ]]; then
  fail "the working tree must be clean; commit or remove local source changes before releasing."
fi

commit="$(git -C "$repo_root" rev-parse --verify HEAD 2>/dev/null)" || fail "HEAD is not a releasable commit."
printf 'release preflight passed for commit %s\n' "${commit:0:12}"
