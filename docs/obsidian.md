---
title: "Obsidian Domain"
status: draft
purpose: "Define the Obsidian MCP server contract and vault-safe filesystem-like read tools."
covers:
  - internal/tools/obsidian/
  - internal/fsx/
  - docs/requirements/obsidian-filesystem-tools.md
---

# Obsidian Domain

The `obsidian` MCP server exposes read-only, filesystem-like tools over one configured local vault. It should feel natural for ChatGPT to navigate notes, but correctness must not depend on hidden server-side state.

## Tool Vocabulary

Initial tool names:

- `ls`
- `read`
- `grep`
- `search`
- `stat`
- `resolve`

The MCP server name is the public integration boundary. Do not prefix tool names with `obsidian.` inside this server, and do not add non-Obsidian tools to this server.

Implemented first-slice tools:

- `resolve`: normalize explicit vault-relative `path` plus optional `base`, report existence and type, and deny unsafe paths with sanitized structured tool errors.
- `ls`: list one directory level only, with deterministic ordering, hidden-entry filtering, symlink non-traversal, and a maximum limit of 500 entries.

Not implemented yet: `read`, `grep`, `search`, and `stat`.

## Stateless Path Model

Tools should accept explicit path context:

```json
{ "path": "home/projects", "base": "", "limit": 100 }
```

`base` may give shell-like ergonomics, but it is an input, not server session state. A later model turn can reuse a returned base explicitly. Server-side `cd` state is not part of the first design.

`resolve` owns path normalization and can return canonical vault-relative paths for follow-on calls.

## Vault Boundary

All paths are vault-relative after normalization. The filesystem adapter must reject path traversal, symlink escapes, absolute paths unless explicitly admitted by config, hidden local databases, secret directories, and any file outside the configured vault root.

## Search Strategy

Phase 1 should prefer bounded live filesystem operations and keep two search surfaces separate:

- `search` should start as filename/path/title-oriented lookup for navigation.
- `grep` should own bounded content search.

`ripgrep` may be used as an implementation detail if available and wrapped with timeouts, output limits, and root confinement. It is an optional fast path, not a required global install. Keep a Go fallback for correctness and tests. Do not add a persistent indexer until live proof shows it is needed.

## Current Gaps

- `GAP-OBS-001`: `read`, `grep`, `search`, and `stat` schemas and handlers are not implemented.
- `GAP-OBS-002`: ChatGPT app installation, simple tool-name discovery, and
  read-only action classification are proven. A bounded model-driven `ls` call
  is proven in ChatGPT, and model-driven `resolve` is proven through Codex using
  the installed app. A ChatGPT-web-specific `resolve` call is not independently
  proven.
- `GAP-OBS-003`: Root confinement and path-denial proof must be extended when future read, search, grep, and stat tools are implemented.
- `GAP-OBS-004`: Search performance over the real vault has not been measured.
