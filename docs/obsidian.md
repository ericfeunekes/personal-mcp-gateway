---
title: "Obsidian Domain"
status: draft
purpose: "Define the Obsidian MCP server contract for vault-safe discovery, bounded reading, and authored-reference traversal."
covers:
  - internal/tools/obsidian/
  - internal/fsx/
  - docs/requirements/obsidian-filesystem-tools.md
---

# Obsidian Domain

The `obsidian` MCP server exposes read-only agent tools over one configured local vault. The surface favors familiar discovery, bounded source reading, and explicit graph operations. Correctness must not depend on hidden server-side state.

## Tool Vocabulary

Target tool names:

- `ls`
- `resolve`
- `read`
- `read_many`
- `grep`
- `links`
- `traverse`
- `backlinks`
- `path_between`

The MCP server name is the public integration boundary. Do not prefix tool names with `obsidian.` inside this server, and do not add non-Obsidian tools to this server. Do not expose separate `search`, `stat`, `graph_search`, shell, or generic query tools: `grep` is the content-discovery entry point, and `resolve` owns metadata.

The target list is phased. `tools/list` advertises only fully implemented and proven tools, never disabled placeholders. `backlinks` and `path_between` remain absent until the numeric full-vault activation gate in the requirements passes.

Implemented first-slice tools:

- `resolve`: return canonical stored-spelling/NFC identity and metadata for explicit vault-relative `path` plus optional `base`, including successful `exists:false` results for missing paths.
- `ls`: list one directory level in canonical order with hidden-entry filtering, symlink non-traversal, a maximum limit of 500 entries, stateless source/query-bound cursors, truthful coverage, and a 64 KiB SDK-result cap.

The local implementation derives registration, schemas, backend-ready names, and safe telemetry from one descriptor authority. Filesystem access is fd-anchored per operation, pagination re-scans the complete shallow directory while retaining only `O(limit)` candidates, and JSONL/SQLite summaries cannot retain raw paths, entry names, cursor values, or content. Phase 1 proof now covers candidate smoke, current-vault and stratified performance, ten cold processes, repeated-call CPU/memory/descriptor bounds, a 60-second idle window, refreshed authenticated metadata, model-selected two-page cursor continuation, and exact-candidate release acceptance.

The 64 KiB encoded SDK result limit is the absolute context envelope for every Obsidian tool. Phase 2 retrieval may accept source/content work budgets up to 256 KiB, but it must page any larger selected work beneath that envelope with caller-carried cursors. Single-note Markdown parsing is capped at 8 MiB and 50,000 physical source lines so source-unit selection remains memory-bounded without another agent-facing option. `grep` likewise rejects an examined physical line larger than 1 MiB instead of buffering unbounded line evidence. Retrieval uses one shared coverage grammar; `grep` favors useful early pages and reports incomplete scope rather than continuing an expensive scan only to strengthen a completeness claim.

Not implemented yet: `read`, `read_many`, `grep`, `links`, `traverse`, `backlinks`, and `path_between`.

## Stateless Path Model

Tools should accept explicit path context:

```json
{ "path": "home/projects", "base": "", "limit": 100 }
```

`base` may give shell-like ergonomics, but it is an input, not server session state. A later model turn can reuse a returned base explicitly. Server-side `cd` state is not part of the first design.

`resolve` owns path normalization and can return canonical vault-relative paths for follow-on calls.

## Vault Boundary

All tool paths are vault-relative after normalization. The filesystem adapter must reject absolute tool inputs, path traversal, symlink escapes, hidden local databases, secret directories, and any file outside the configured vault root. Only process startup config may supply the absolute vault root. Content and reference tools operate on Markdown files; `ls` and `resolve` may still report safe attachment metadata.

## Retrieval Strategy

Use composable operations rather than one overloaded search tool:

- `grep` finds source evidence by content with deterministic path and line provenance.
- `read` extracts one explicit source unit; `read_many` batches a known working set.
- `links` parses and locally resolves outbound authored references from one note without a vault-wide scan.
- `traverse` builds a bounded request-local catalog of Markdown paths inside explicit scopes, then reads reached notes lazily with shallow defaults.
- `backlinks` and `path_between` use live request-local whole-scope scans and remain unadvertised until their full-vault performance gate passes.

`ripgrep` may be used as an implementation detail if its regex and ordering semantics match the implementation fallback. It is an optional fast path, not a required global install. Do not add a persistent indexer until full-vault measurement shows it is needed and freshness, invalidation, recovery, and ownership requirements are settled.

## Graph And Coverage Model

Graph edges are authored Obsidian wikilinks and Markdown links. Heading and block fragments are edge attributes; tags, shared properties, aliases, embeddings, and semantic similarity are not resolution inputs or edges. Missing, unresolved, ambiguous, external, and disallowed references remain visible boundary evidence. External targets are never fetched.

Outbound traversal may cross exercise, health, initiative, food, and concept folders while remaining vault-confined. Explicit `scopes` decide which reached targets may be expanded and which files inbound operations may scan. Agents request backlinks separately rather than enabling bidirectional expansion by default.

Every scan or graph result reports both result completeness and declared-query completeness, plus work performed and the budget that stopped it. Lower work budgets do not redefine scope. A negative answer is conclusive only for the declared scopes and maximum depth when that complete query was examined. Deterministic limits provide a stateless cursor; timeout, cancellation, or source change requires a restart. No server-side working directory or graph session is required.

The detailed schemas, limits, resolution rules, acceptance criteria, and performance gates live in `docs/requirements/obsidian-filesystem-tools.md`.

## Current Gaps

- `GAP-OBS-001`: `read`, `read_many`, and `grep` are not implemented.
- `GAP-OBS-002`: ChatGPT and Codex proof covers the current `ls`/`resolve` surface only; the expanded agent workflow is not proven live.
- `GAP-OBS-003`: Root confinement, denial, read-only, cursor, and sanitized-error proof is complete for the Phase 1 `resolve`/`ls` surface; equivalent proof is not yet extended to content, batch, reference, and graph operations.
- `GAP-OBS-004`: Full-vault grep, backlink, and path-discovery latency, scan work, response size, and freshness trade-offs are not measured. The bounded exercise/health/marathon spike is complete.
- `GAP-OBS-005`: Existing tunnel/runtime proof remains valid, but live metadata refresh and representative model-selected calls must be repeated after the tool list expands.
- `GAP-OBS-006`: `links`, the scoped request-local path catalog, and outbound `traverse` are not implemented.
- `GAP-OBS-007`: The full-vault activation gate for live request-local `backlinks` and `path_between` has not been run or passed; the tools are neither implemented nor advertised.
- `GAP-OBS-008`: Descriptor-owned safe telemetry summaries are complete for the Phase 1 `resolve`/`ls` surface; summaries for read, grep, batch, links, traversal, backlinks, and path discovery are not implemented.
- `GAP-OBS-009`: Phase 1 `resolve`/`ls` summaries have local JSONL and SQLite proof, including accepted model-driven `ls` telemetry; summaries for newly activated retrieval and graph tools have not been proven.
- `GAP-OBS-010`: Live request-local `backlinks` and `path_between` are not implemented for pre-activation benchmark and proof.
