#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
make_command="${MAKE:-make}"

fail() {
  printf 'personal-mcp-gateway update: %s\n' "$1" >&2
  exit 1
}

branch="$(git -C "$repo_root" branch --show-current)" || fail "unable to determine the current branch."
if [[ "$branch" != "main" ]]; then
  fail "updates must run from the main branch."
fi
status="$(git -C "$repo_root" status --porcelain --untracked-files=all)" || fail "unable to inspect the working tree."
if [[ -n "$status" ]]; then
  fail "the working tree must be clean before updating."
fi

printf 'update: fetching origin\n'
git -C "$repo_root" fetch origin
printf 'update: fast-forwarding main\n'
git -C "$repo_root" merge --ff-only origin/main
head_commit="$(git -C "$repo_root" rev-parse HEAD)" || fail "unable to resolve local main."
origin_commit="$(git -C "$repo_root" rev-parse origin/main)" || fail "unable to resolve origin/main."
if [[ "$head_commit" != "$origin_commit" ]]; then
  fail "local main must exactly match origin/main before release."
fi
printf 'update: releasing updated main\n'
exec "$make_command" --no-print-directory -C "$repo_root" release
