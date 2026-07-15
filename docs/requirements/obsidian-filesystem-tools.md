---
title: "Obsidian Agent Tools Requirements"
status: draft
purpose: "Define the read-only Obsidian MCP tool surface for efficient agent discovery, bounded reading, and authored-reference traversal."
covers:
  - internal/tools/obsidian/
  - internal/fsx/
  - docs/obsidian.md
---

# Obsidian Agent Tools Requirements

## Desired Outcome

An agent can efficiently discover relevant notes, read only the needed portions, follow authored Obsidian references with provenance, and batch explicit reads without hidden state or unbounded vault work. Every result is directly composable into the next call. Every partial search or traversal result says whether the returned results and the declared search scope are complete. Representative real-vault workflows meet explicit latency, scan-work, response-size, and idle-impact targets.

The gateway remains a read-only adapter. The Obsidian vault is the source of truth.

## Why The Surface Changes

The current server implements `resolve` and shallow `ls`. Earlier planning named `read`, `grep`, `search`, and `stat`, but did not define their public grammar or graph behavior. Live agent-oriented exploration of workout, exercise, and health notes showed three distinct retrieval needs:

- content discovery works best as familiar `grep`;
- graph-sparse corpora need bounded `read_many` after discovery;
- authored evidence chains benefit strongly from outbound links and traversal, while inbound expansion is useful but substantially broader and more expensive.

`search` is therefore removed in favor of `grep`. `stat` is removed because `resolve` already owns existence, type, size, and modified-time metadata. One ambiguous `graph_search` tool is rejected in favor of explicit composable graph operations.

## User-Owned Decisions

- Agent ease of use and performance outrank matching a traditional filesystem command inventory.
- The broad target vocabulary includes first-class `backlinks` and `path_between`, but their implementation and activation remain performance-gated.
- The agent is the primary user and may choose graph scope based on the task. Outbound references may cross a domain boundary while remaining inside the vault; explicit scopes control which targets are expanded or scanned.
- Requirements are informed by real workout/exercise/health workflows rather than invented examples alone.

## Public Tool Vocabulary

The MCP server is named `obsidian`. Tools inside it use these simple names:

- `resolve`
- `ls`
- `read`
- `read_many`
- `grep`
- `links`
- `traverse`
- `backlinks`
- `path_between`

Do not prefix tool names with `obsidian.` inside this server. Do not expose `search`, `stat`, `graph_search`, shell commands, generic filesystem operations, or generic query execution.

Every activated tool advertises read-only, non-destructive, closed-world MCP annotations. Enforcement remains in the implementation and vault boundary, not the annotations.

This is the target vocabulary, not a requirement to advertise unfinished or disabled tools. `tools/list` contains only activated tools whose complete contract is implemented and proven. There are no placeholder tools that return "not enabled":

- current activation: `resolve`, `ls`;
- core retrieval activation: add `read`, `read_many`, `grep` together after their local proof passes;
- outbound graph activation: add `links`, `traverse` together after reference-resolution, traversal, and local performance proof passes;
- inbound graph activation: add `backlinks`, `path_between` only after the full-vault gate below passes.

Once activated, a tool is available by default; this requirement does not add a runtime feature flag or alternate contract.

The currently advertised `resolve` and `ls` implementations satisfy the Phase 1 identity, continuation, response-budget, coverage, descriptor, telemetry, performance, process-resource, idle, authenticated model-journey, and exact release-acceptance contracts below. Later activation groups must repeat their own relevant local, resource, metadata-refresh, and model-selected proof before their tools are advertised.

## Common Request And Result Contract

Tool paths are always vault-relative. Absolute tool inputs are rejected; only the process-level vault-root configuration is absolute. `path` is required where one target is needed; optional `base` is an explicit vault-relative resolution context. No call depends on server-side current-directory or conversational state.

Content and reference tools operate on Markdown (`.md`) files only. `resolve` and `ls` may report other non-hidden, non-symlink entries as metadata, and `links` may report attachment edges, but this surface does not read or parse attachment content.

Graph operations accept `scopes`, optional vault-relative roots with default `["."]`. `grep` instead accepts one familiar `path`, default `"."`. Scanning and graph operations accept:

- the tool-specific result, node, edge, and depth limits below;
- `cursor`: an opaque, self-contained continuation bound to the normalized query;
- optional lower `max_files` and `max_bytes` work budgets. Scan defaults are 10,000 files and 256 MiB; hard maxima are 50,000 files and 1 GiB.

A cursor must not depend on hidden server session state and must fit within 16 KiB. It binds the normalized query, deterministic ordering position or graph frontier, and source fingerprints needed to detect staleness. A mismatched, malformed, or detectably stale cursor returns a structured error rather than silently restarting or changing query semantics.

Canonical paths preserve the vault's stored spelling, use `/` separators, and compare in Unicode NFC. For a missing target, every existing prefix segment uses its stored spelling and each nonexistent suffix segment uses the caller's NFC-normalized spelling. Ordering is ascending UTF-8 byte order of those NFC paths, then the tool-specific location tie-breakers. Exact-case matches win; a case-folded match is accepted only when unique among the examined candidates.

Every scan or graph result includes a `coverage` object:

- `result_complete`: whether all discovered results fit the output limit;
- `scope_complete`: whether every file or frontier required by the declared path/scopes and query boundary was examined, regardless of the caller's work budget;
- `consistency`: `stable` when the operation observed no source change, otherwise `best_effort`;
- `files_scanned` and `bytes_scanned` where a filesystem scan occurred;
- `stopped_by`: bounded reasons such as `result_limit`, `file_limit`, `byte_limit`, `node_limit`, `edge_limit`, `timeout`, `canceled`, `scope`, or `source_changed`;
- `continuation`: `complete`, `cursor`, or `restart`, plus `next_cursor` when its value is `cursor`.

Lower work budgets never redefine the declared scope. Hitting a work, node, edge, timeout, cancellation, or source-change boundary makes `scope_complete` false. A result limit may leave `scope_complete` true only if the operation still examined the complete declared query. `max_depth` is part of the query boundary: a complete negative path result means no path within that depth, not no path at any depth.

A partial success stopped by a deterministic limit must include a cursor. Timeout, cancellation, or source change returns `continuation: restart` and no conclusive negative claim. An operation that cannot safely provide either continuation form returns a structured error rather than a lossy partial success.

`found: false`, no backlinks, or no path is conclusive only for the reported path/scopes and query boundary when `scope_complete` is true. A query over scopes smaller than `["."]` never implies a vault-wide negative.

Errors are structured tool results with stable codes and sanitized messages. Raw host paths, vault roots, credentials, note content, and query text are never placed in telemetry or process diagnostics.

Common public error codes are:

- `path_denied`, `symlink_denied`, `not_found`, `not_directory`, `unsupported_file`, and `invalid_utf8` for vault/file boundaries;
- `invalid_selector` and `invalid_regex` for tool grammar;
- `limit_exceeded`, `input_too_large`, and `response_too_large` for resource contracts;
- `cursor_invalid` for malformed, oversized, or unsupported-version cursors;
- `cursor_mismatch` when a cursor does not belong to the normalized query;
- `cursor_stale` when a resumed source or catalog fingerprint no longer matches;
- `source_changed` when mutation is observed during an active operation;
- `timeout` and `canceled` for request termination.

Tool-specific seed/item errors reuse these codes. Messages remain generic and sanitized; callers branch on the code, not message text.

## Tool Contracts

### `resolve`

Input: `path`, optional `base`.

Returns the canonical vault-relative path, existence, type, size, and modified time. Missing paths are valid resolved results: their deepest existing ancestor uses stored spelling and the remaining suffix uses NFC-normalized caller spelling. Unsafe inputs are errors. This is the metadata operation; there is no separate `stat` tool.

### `ls`

Input: `path`, optional `base`, `limit` (default 100, hard maximum 500), and optional `cursor`.

Lists one directory level in canonical path order. Results contain canonical paths, type, size, and modified time. Hidden entries are excluded, symlinks are never traversed, and truncation provides stateless continuation. Recursive directory walking is not part of `ls`.

### `read`

Input: `path`, optional `base`, optional `selector`, optional lower `max_bytes`, and optional `cursor`.

`selector.kind` is exactly one of:

- `content`: bounded content beginning at `start_line` (default 1);
- `heading`: one NFC, trimmed, case-sensitive Markdown heading plus one-based occurrence (default 1);
- `block`: one exact Obsidian block ID, supplied without the leading `^`;
- `frontmatter`: the leading YAML frontmatter block only;
- `outline`: headings with levels and source lines, without body content.

Conflicting selector fields are invalid. The default is `content` from the beginning of the file. Results contain canonical path, selected line range, total known lines when available, content or outline data, modified time, a content fingerprint, truncation, and continuation.

A first call supplies the path and selector without a cursor. A continued call repeats the same normalized path, selector, and byte cap plus the returned cursor. The cursor binds those inputs, the selected source-unit boundary, the next unread position, and the source fingerprint. A changed query returns `cursor_mismatch`; a changed source returns `cursor_stale`. This prevents an agent from silently joining content from two versions of a note. Truncated heading, block, frontmatter, outline, and content selections all use this continuation contract rather than an unguarded `start_line` alone.

Only UTF-8 Markdown files are readable. Invalid UTF-8, other extensions, directories, symlinks, and disallowed paths return structured errors. The default response cap is 64 KiB; the hard per-call cap is 256 KiB.

### `read_many`

Input: `requests`, an ordered list of up to 20 first-call `read` request shapes, an optional lower aggregate byte cap, and optional `cursor`.

Results preserve input order and carry each item's original request index. One denied, missing, or unsupported item produces an item-level error without discarding successful items. Top-level schema errors fail the call. The aggregate content cap defaults to 64 KiB and has a caller-selectable hard maximum of 256 KiB. Requests are processed in order.

When an item or aggregate cap leaves content unread, the batch returns one cursor binding the complete normalized ordered request list, aggregate cap, current request index, per-item continuation position, and observed fingerprints, plus `next_request_index` and `remaining_request_count` hints. A continued call repeats the original ordered requests and aggregate cap with that cursor; changing them returns `cursor_mismatch`, while a changed already-read or current source returns `cursor_stale`. Items after an exhausted aggregate cap are not opened and are represented by the continuation metadata rather than terminal item errors.

### `grep`

Input: `pattern`, optional `path` and `base`, optional `regex` (default true), optional `case_sensitive` (default false), `context_lines` (default 1, maximum 3), `limit`, optional work budgets, and optional `cursor`.

`grep` searches Markdown contents only. It does not perform filename ranking, semantic similarity, or graph expansion. Frontmatter, aliases, and headings are searchable because they are content. Regex mode uses Go RE2 syntax as accepted by `regexp.Compile`; invalid patterns fail before scanning. An optional `ripgrep` fast path may handle only patterns proven equivalent by parity tests and must otherwise fall back to Go.

The result unit and `limit` unit are matching lines, not files or individual occurrences. Results are ordered by canonical path, one-based line, then first one-based match column. Each result carries the path, line, first column, number of non-overlapping occurrences on that line, bounded context, and content fingerprint. Context lines do not consume the result limit. Default result limit is 50; hard maximum is 200. The default response cap is 64 KiB and the hard cap is 256 KiB.

### `links`

Input: `path`, optional `base`, `limit` (default 100, hard maximum 500), `max_source_bytes` (default 512 KiB, hard maximum 2 MiB), and optional `cursor`.

Parses outbound authored references from one Markdown note without a vault-wide scan. Occurrences are ordered by source line, column, then syntax. Each includes source line/column, syntax (`wikilink` or Markdown link), edge kind (`note`, `embed`, `attachment`, or `external`), written target, fragment, and resolution status.

Resolution status is one of `resolved`, `missing`, `unresolved`, `external`, `disallowed`, or `malformed`. Resolved targets include canonical vault-relative paths. `missing` is used only when a path-addressed target was examined and absent. A bare target that may exist elsewhere is `unresolved`, never falsely `missing`. External URLs may be reported but are never fetched.

Heading and block fragments are edge attributes, not graph nodes. Tags and inferred frontmatter relationships are not graph edges in this requirement. Literal link examples inside fenced code, inline code, or HTML comments are not references.

Single-note resolution is intentionally local and ordered:

1. external URL syntax becomes `external`;
2. Markdown paths and `./` or `../` targets resolve relative to the source directory after valid percent-decoding;
3. a leading `/` or a wikilink containing `/` resolves as a vault-relative path;
4. `.md` may be omitted;
5. a bare target resolves only to an exact or uniquely case-folded Markdown filename in the source directory; otherwise it remains `unresolved`.

Unsafe normalized targets become `disallowed`. `links` does not scan for unique vault-wide basenames or aliases. Alias resolution is outside this requirement.

### `traverse`

Input: `paths` (one to ten canonical or resolvable seed notes), optional `scopes`, `max_depth` (default 1, hard maximum 3), `max_nodes` (default 25, hard maximum 100), `max_edges` (default 50, hard maximum 200), optional `edge_kinds` (default `["note", "embed"]`), optional lower scan budgets, and optional `cursor`.

Traversal follows outbound authored references only. At request start it builds a bounded, metadata-only catalog of Markdown paths inside the declared scopes; it reads note content only for reached nodes. That catalog resolves path-addressed targets and a bare target only when its basename is unique in the completely examined catalog. Duplicate matches are `ambiguous` with at most ten candidates in canonical order; if catalog coverage is incomplete, unresolved bare targets stay `unresolved`. Aliases are not resolution inputs.

Traversal is deterministic breadth-first search. Seed order follows the request; each node's outgoing occurrences use `links` order, with resolved target path as the final tie-breaker. Results contain canonical nodes, provenance-carrying directed edges, unresolved/ambiguous boundary references, coverage, and a self-contained continuation cursor when bounded work remains.

Missing, disallowed, non-Markdown, or out-of-scope seeds produce ordered seed-level errors while valid seeds continue; if no seed is valid, the tool returns a structured error and no graph. Only resolved Markdown `note` and `embed` targets can expand. Attachment and external edges may be requested as boundary evidence but never become expandable nodes.

Targets outside the declared scopes may be reported as boundary nodes but are not expanded. With default scope `["."]`, outbound traversal may cross exercise, health, initiative, food, or concept domains while remaining vault-confined. Cycles are deduplicated deterministically.

Inbound expansion is deliberately not a `traverse` mode. Agents call `backlinks` explicitly when they want the broader and more expensive inbound view.

### `backlinks`

Input: `path`, optional `base`, optional `scopes` (default `["."]`), `limit` (default 50, hard maximum 200), optional lower scan budgets, and optional `cursor`.

Performs a live, request-local scan of Markdown files inside the declared scopes and returns authored references resolving to the target. Results are ordered by source canonical path, line, and column; each carries occurrence provenance. It uses the same scoped path catalog and no aliases. Negative answers are conclusive only when `scope_complete` is true.

`backlinks` is a first-class target tool but remains unadvertised until the full-vault activation gate passes. The first permitted strategy is the live request-local scan above. A cache or persistent index requires a later requirements/ADR change and may not be introduced as an implementation fallback.

### `path_between`

Input: `from`, `to`, optional `scopes` (default `["."]`), `max_depth` (default 4, hard maximum 6), `max_nodes` (default 250, hard maximum 1,000), `max_edges` (default 500, hard maximum 2,000), optional `edge_kinds` (default `["note", "embed"]`), optional lower scan budgets, and optional `cursor`.

Builds a live request-local authored-reference graph inside the scopes and returns one shortest directed path with edge provenance. Search is breadth-first; equal-length paths are broken by the lexicographically smallest sequence of canonical paths. Only Markdown note/embed edges can connect endpoints. A missing, disallowed, non-Markdown, or out-of-scope endpoint is a structured error, not `found: false`. It does not return semantic-similarity paths. `found: false` is conclusive only for the declared scopes and `max_depth` when `scope_complete` is true. It remains unadvertised until the same activation gate as `backlinks` passes.

## State, Authority, And Performance

The vault is authoritative. Live parsing and scanning produce bounded read projections. No persistent/background index, startup scan, filesystem watcher, or derived durable graph is required by this contract.

The requirements spike used a representative 47-file, approximately 231 KiB exercise/health/marathon corpus. Direct grep completed in roughly 12-41 ms, request-local graph construction after content load in roughly 1-3 ms, and the broader path/identity load in roughly 110-596 ms across observed cold/warm runs. These numbers are planning evidence, not implementation proof.

Initial performance expectations:

- `resolve`, shallow `ls`, bounded single-file `read`, and `links` over a note within the default source-byte cap should normally complete within 100 ms on local cached files;
- scoped `grep` over approximately 50 files / 250 KiB should complete within 250 ms;
- outbound depth-two traversal capped at 50 nodes should complete within 750 ms including cold request-local preparation;
- every operation remains under the current two-second hard timeout unless a later requirement changes it;
- default scan/graph responses remain at or below 64 KiB, with deterministic continuation instead of oversized payloads;
- startup performs no whole-vault walk, and idle runtime performs no background parsing.

Full-vault `grep` requires separate measurement but may activate earlier because scoped grep remains useful and coverage is explicit. `backlinks` and `path_between` remain absent from `tools/list` until the live request-local strategy passes all of this gate on the current vault:

- at least 20 representative full-vault calls, with at least ten calls per gated tool and at least five distinct targets overall, including positive, negative, duplicate-basename, and cross-domain cases;
- default scan budgets cover the complete current vault for every measured call;
- p95 latency is at most 1.5 seconds, no call exceeds the two-second hard timeout, and cancellation stops within 100 ms;
- default responses remain at or below 64 KiB and every bounded partial has a valid continuation;
- each call reports actual files/bytes scanned, nodes/edges examined, response bytes, and consistency in its result coverage, with the same bounded counters summarized safely in telemetry;
- source content is read live during the call, no source projection older than call start is reused, and an observed source change forces `continuation: restart`;
- repeated calls add no more than 64 MiB peak RSS above the pre-call baseline and return within 10 percent of that baseline within 30 seconds;
- the 20-call set includes three four-call concurrent rounds (two calls per gated tool): cold positive queries, warm negative/duplicate-basename queries, and a mixed-cancellation round. Four additional sequential calls per tool complete the minimum ten calls per tool. Every non-canceled call stays within the same per-call timeout and memory bounds, cancellation of one call does not cancel or corrupt the others, and aggregate files/bytes scanned are reported rather than hidden by shared mutable state;
- startup and idle behavior remain scan-free, with no watcher, cache refresh, or background graph work.

If this gate fails, the tools stay unadvertised and `GAP-OBS-007` remains open. A future cache or index is a derived projection and requires a new requirements/ADR decision covering freshness, maximum staleness, invalidation, rebuild, corruption recovery, readiness, telemetry, and ownership before it can become response-authoritative.

## Important Non-Goals

- Write, edit, create, delete, move, or rename tools.
- Generic host filesystem access, shell execution, or generic HTTP fetching.
- Semantic/vector search or inferred relatedness.
- Tags, shared properties, or embeddings treated as graph edges.
- A graph database as a first implementation requirement.
- A background indexer, startup scan, filesystem watcher, or hidden server-side navigation state.
- Full Obsidian application parity for every plugin-defined link or property syntax.
- Fetching or following external URLs returned by `links`.

## Acceptance Criteria

- `AC-OBS-001`: MCP `tools/list` advertises exactly the tools activated for the completed phase, with no disabled placeholders; every listed tool has read-only, non-destructive, closed-world annotations and schemas that reject missing required inputs or conflicting selectors. Proof: `docs/TESTING.md` MCP tool-registration and schema boundary cell plus live connector tool-list proof.
- `AC-OBS-002`: Every path, scope, cursor, link target, and resolved candidate remains confined to the configured vault; traversal, hidden paths, and symlink escapes are denied without leaking host paths. Proof: vault path-confinement cell with synthetic link, scope, cursor, traversal, and symlink fixtures.
- `AC-OBS-003`: Reads, batches, scans, and graph operations enforce result, file, byte, node, edge, depth, response, and time budgets; cancellation stops work promptly. Proof: search/listing-limits cell with large fixture vaults and cancellation/timeout cases.
- `AC-OBS-004`: No tool mutates the vault, including failure, timeout, cancellation, partial batch, and malformed-reference paths. Proof: read-only integration cell with before/after fixture-vault snapshots.
- `AC-OBS-005`: ChatGPT/Codex can discover and select representative tools through OpenAI Secure MCP Tunnel. Model-driven journey proof covers `grep` → `read_many` → continued batch reading and `links` → `traverse` → continued traversal before the expanded surface is considered settled. Proof: live Obsidian tool-name/model-use cell.
- `AC-OBS-006`: Startup and idle behavior remain scan-free and quiet, and representative performance measurements record latency, files/bytes scanned, response size, CPU, memory, descriptors, and cancellation behavior. Proof: minimal-machine-impact cell.
- `AC-OBS-007`: `grep` searches content with deterministic line evidence and does not silently rank filenames, perform semantic search, or expand graph neighbors. Proof: tool boundary and search-limit tests using the same corpus for grep and graph calls.
- `AC-OBS-008`: `read` selectors return the requested bounded source unit with provenance, and `read_many` preserves input order, isolates item errors, and enforces aggregate caps. Proof: MCP boundary tests plus filesystem fixture tests for headings, duplicate headings, block IDs, frontmatter, outlines, UTF-8, binary files, truncation, and partial batch failure.
- `AC-OBS-009`: `links` distinguishes resolved, missing, unresolved, external, disallowed, and malformed references with source locations; scoped traversal additionally distinguishes ambiguous basenames with deterministic bounded candidates. Neither resolves aliases or fetches external targets. Proof: parser unit/property tests plus filesystem/MCP boundary fixtures.
- `AC-OBS-010`: `traverse`, `backlinks`, and `path_between` preserve directed edge provenance, deterministic ordering, cycle deduplication, explicit scopes, and resumable bounded work; they never report a conclusive negative when scope coverage is incomplete or beyond the declared maximum depth. Proof: graph fixture boundary tests with cycles, duplicate basenames, missing targets, cross-scope edges, cursors, truncation, timeout, and source-change cases.
- `AC-OBS-011`: The initial performance expectations are met on the representative corpus, and `backlinks`/`path_between` are absent until every numeric full-vault activation criterion passes. Proof: search/listing-limits and minimal-machine-impact cells with a retained aggregate benchmark record and no personal note content.
- `AC-OBS-012`: JSONL/SQLite telemetry records safe tool, outcome, latency, bounded counts, scan-work, truncation, and coverage summaries without raw paths, patterns, cursors, link text, note content, snippets, or candidate names. Proof: structured-telemetry proof matrix.

## Dependencies And Context Route

Authoritative first reads:

- `docs/ARCHITECTURE.md` for process, state, dependency, and no-index boundaries;
- `docs/obsidian.md` for the domain vocabulary and graph strategy;
- `docs/decisions/ADR-2026-07-10-obsidian-agent-tool-surface.md` for the accepted vocabulary decision;
- `docs/TESTING.md` for proof cells;
- `docs/gateway.md` for telemetry and shared limits.

Conditional reads:

- `docs/runbooks/openai-tunnel.md` only for live connector proof;
- accepted ADRs under `docs/decisions/` when changing server/tool naming or MCP SDK assumptions.

The scratch traversal prototype is non-authoritative and must not override this requirement after its findings are promoted.

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
