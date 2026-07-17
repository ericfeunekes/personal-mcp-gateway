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

The current accepted server implements `resolve`, shallow `ls`, bounded `read`, aggregate-budgeted `read_many`, and deterministic content `grep`. Earlier planning named `search` and `stat` without defining their public grammar or graph behavior. Live agent-oriented exploration of workout, exercise, and health notes showed three distinct retrieval needs:

- content discovery works best as familiar `grep`;
- graph-sparse corpora need bounded `read_many` after discovery;
- authored evidence chains benefit strongly from outbound links and traversal, while inbound expansion is useful but substantially broader and more expensive.

`search` is therefore removed in favor of `grep`. `stat` is removed because `resolve` already owns existence, type, size, and modified-time metadata. One ambiguous `graph_search` tool is rejected in favor of explicit composable graph operations.

## User-Owned Decisions

- Agent ease of use and performance outrank matching a traditional filesystem command inventory.
- The encoded SDK `CallToolResult` remains absolutely bounded to 64 KiB for every tool. A larger caller-selected content or scan budget never widens the model-context envelope; deterministic continuation pages the work instead.
- `grep` returns useful evidence as soon as it reaches a result, response, file, or byte boundary. It does not keep scanning merely to turn a partial page into a complete-scope claim; coverage and continuation report the boundary truthfully.
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

Every encoded SDK `CallToolResult`, including text content plus structured content, is at most 64 KiB. Domain fitting reserves enough space for the SDK text shell and returns a cursor before structured content can exceed the remaining envelope. The 256 KiB hard limits below are source/content work budgets, not permission to emit a larger SDK result.

This is the target vocabulary, not a requirement to advertise unfinished or disabled tools. `tools/list` contains only activated tools whose complete contract is implemented and proven. There are no placeholder tools that return "not enabled":

- current activation: `resolve`, `ls`, `read`, `read_many`, `grep`;
- outbound graph activation: add `links`, `traverse` together after reference-resolution, traversal, and local performance proof passes;
- inbound graph activation: add `backlinks`, `path_between` only after the full-vault gate below passes.

Once activated, a tool is available by default; this requirement does not add a runtime feature flag or alternate contract.

The currently advertised five-tool surface satisfies the Phase 1 identity/listing and Phase 2 core-retrieval continuation, response-budget, coverage, descriptor, telemetry, performance, process-resource, idle, authenticated model-journey, and exact release-acceptance contracts below. Later graph activation groups must repeat their own relevant local, resource, metadata-refresh, and model-selected proof before their tools are advertised.

Phase 2 proof is layered rather than one all-purpose real-vault test: synthetic fixtures prove retrieval semantics, current-vault probes prove broad `grep`, inventory, performance, and resource behavior, and the authenticated model journey proves live grouped retrieval and continuation.

## Common Request And Result Contract

Tool paths are always vault-relative. Absolute tool inputs are rejected; only the process-level vault-root configuration is absolute. `path` is required where one target is needed; optional `base` is an explicit vault-relative resolution context. No call depends on server-side current-directory or conversational state.

Content and reference tools operate on Markdown (`.md`) files only. `resolve` and `ls` may report other non-hidden, non-symlink entries as metadata, and `links` may report attachment edges, but this surface does not read or parse attachment content.

Graph operations accept `scopes`, optional vault-relative roots with default `["."]`. `grep` instead accepts one familiar `path`, default `"."`. Scanning and graph operations accept:

- the tool-specific result, node, edge, and depth limits below;
- `cursor`: an opaque, self-contained continuation bound to the normalized query;
- optional lower `max_files` and `max_bytes` work budgets. Scan defaults are 10,000 files and 256 MiB; hard maxima are 50,000 files and 1 GiB.

A cursor must not depend on hidden server session state and must fit within 16 KiB. It binds the normalized query, deterministic ordering position or graph frontier, and source fingerprints needed to detect staleness. A mismatched, malformed, or detectably stale cursor returns a structured error rather than silently restarting or changing query semantics.

Canonical paths preserve the vault's stored spelling, use `/` separators, and compare in Unicode NFC. For a missing target, every existing prefix segment uses its stored spelling and each nonexistent suffix segment uses the caller's NFC-normalized spelling. Ordering is ascending UTF-8 byte order of those NFC paths, then the tool-specific location tie-breakers. Exact-case matches win; a case-folded match is accepted only when unique among the examined candidates.

Every partial-capable retrieval, scan, or graph result includes the same `coverage` object:

- `result_complete`: whether all discovered results fit the output limit;
- `scope_complete`: whether every file or frontier required by the declared path/scopes and query boundary was examined, regardless of the caller's work budget;
- `consistency`: `stable` when the operation observed no source change, otherwise `best_effort`;
- `files_scanned` and `bytes_scanned` where content scanning occurred, plus `source_entries_validated` where a continuation revalidated prior source/catalog observations;
- `stopped_by`: bounded reasons such as `result_limit`, `response_limit`, `file_limit`, `byte_limit`, `node_limit`, `edge_limit`, `timeout`, `canceled`, `scope`, or `source_change`; the corresponding public error code is `source_changed`;
- `continuation`: `complete`, `cursor`, or `restart`, plus `next_cursor` when its value is `cursor`.

Lower work budgets never redefine the declared scope. Hitting a work, node, edge, timeout, cancellation, or source-change boundary makes `scope_complete` false. A result limit may leave `scope_complete` true only if the operation still examined the complete declared query. `max_depth` is part of the query boundary: a complete negative path result means no path within that depth, not no path at any depth.

A partial success stopped by a deterministic result, response, file, byte, node, or edge limit must include a cursor. Such a result may set `scope_complete:true` only when the complete declared query was examined. Timeout, cancellation, or source change returns `continuation: restart`, no cursor, and no conclusive negative claim. An operation that cannot safely advance a deterministic cursor returns a structured error rather than an empty or lossy loop.

`found: false`, no backlinks, or no path is conclusive only for the reported path/scopes and query boundary when `scope_complete` is true. A query over scopes smaller than `["."]` never implies a vault-wide negative.

Errors are structured tool results with stable codes and sanitized messages. Raw host paths, vault roots, credentials, note content, and query text are never placed in telemetry or process diagnostics.

Common public error codes are:

- `path_denied`, `symlink_denied`, `not_found`, `not_directory`, `unsupported_file`, and `invalid_utf8` for vault/file boundaries;
- `invalid_selector` and `invalid_regex` for tool grammar;
- `selector_not_found` and `selector_ambiguous` for a valid selector that has no target or more than one target where no occurrence was supplied;
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

`selector` is a closed tagged object whose `kind` is exactly one of:

- `{"kind":"content","start_line":N}`: bounded source beginning at one-based `start_line`, default 1;
- `{"kind":"heading","heading":"...","occurrence":N}`: one NFC-normalized, trimmed, case-sensitive Markdown heading with one-based `occurrence`, default 1;
- `{"kind":"block","block_id":"..."}`: one exact Obsidian block ID supplied without `^`;
- `{"kind":"frontmatter"}`: the leading YAML frontmatter block only;
- `{"kind":"outline"}`: headings with levels and source lines, without body content.

Unknown fields and fields belonging to another selector kind are invalid. The default selector is `{"kind":"content","start_line":1}`. `start_line` and `occurrence` must be positive. `max_bytes` defaults to 64 KiB and may be lowered or raised from 1 byte through 256 KiB; it bounds selected source bytes considered for this call but never widens the absolute 64 KiB SDK result envelope. `read` and `read_many` accept Markdown source files up to 8 MiB and 50,000 physical source lines; exceeding either fixed cap returns `input_too_large`. A non-empty final line without a terminator counts as one physical line. The implementation counts bytes and lines before structural parsing, so over-cap inputs never construct a Markdown AST. These fixed source-complexity caps bound whole-document Markdown parsing without adding another agent-facing tuning knob.

Heading matching recognizes source Markdown headings outside code blocks. The selected unit begins at the matched heading and ends immediately before the next heading of the same or shallower level, so descendant subsections remain part of the unit. A block selector returns the smallest Markdown source block whose final non-whitespace token is the exact `^block_id`; markers inside code are ignored, and duplicate exact block IDs return `selector_ambiguous`. Frontmatter is valid only when a leading `---` delimiter has a closing `---` or `...` delimiter. Outline entries are in source order and contain exactly `line`, `level`, and trimmed heading `text`.

A successful result contains `ok`, canonical `path`, normalized `selector`, `start_line`, `end_line`, optional `total_lines`, `modified`, an opaque SHA-256 `fingerprint`, `truncated`, and `coverage`. The fingerprint identifies the opened source version without requiring a whole-file content read: it hashes the canonical path plus the fd-observed file identity, size, and nanosecond modification/change timestamps (or the platform-equivalent stable source stamp). It exposes none of those host values directly. Exactly one payload field is present: `content` for content, heading, block, and frontmatter selectors, or `outline` for outline. Source text preserves original line endings and spelling. A valid selector that finds no unit returns `selector_not_found`; invalid UTF-8 and non-Markdown inputs return `invalid_utf8` and `unsupported_file`.

A first call supplies the path, selector, and effective byte cap without a cursor. A continued call repeats them unchanged plus the returned cursor. The cursor binds the normalized query, selected source-unit boundary, next unread byte/line or outline-entry position, and source fingerprint. Continuation reopens the canonical path through the confined fd boundary, compares the current source stamp, and seeks to the bound byte position; it does not reread the already returned content merely to validate the cursor. A changed query returns `cursor_mismatch`; a changed source stamp returns `cursor_stale`. This prevents an agent from silently joining content from two versions of a note while keeping continuation work proportional to the new page. Truncated heading, block, frontmatter, outline, and content selections all use this continuation contract rather than an unguarded `start_line` alone.

Only UTF-8 Markdown files are readable. Invalid UTF-8, over-cap source complexity, other extensions, directories, symlinks, and disallowed paths return structured errors. `input_too_large`, timeout, cancellation, or an observed active source change uses `continuation:restart`, never returns a cursor, and never marks unread work as complete. Timeout, cancellation, or source change may include already completed source evidence; an over-cap source is rejected before structural evidence is selected.

### `read_many`

Input: `requests`, an ordered list of one to 20 first-call `read` request shapes (`path`, optional `base`, optional `selector`, optional per-item `max_bytes`), optional aggregate `max_bytes`, and optional `cursor`. Top-level `max_bytes` defaults to 64 KiB and ranges from 1 byte through 256 KiB; it is the aggregate selected-source budget and does not widen the absolute SDK envelope.

Results preserve input order and carry each item's zero-based `index`. Each returned item contains `ok` plus either the same successful source fields as `read` or one structured `error`. One denied, missing, ambiguous, invalid-UTF-8, unsupported, or source-complexity-over-cap item produces an item-level error without discarding successful items. The top-level `ok` remains true when the batch contract completed with item-level errors; it is false only when a top-level schema, cursor, timeout, cancellation, source-change, response-fit, or aggregate failure is present. Requests are processed strictly in order; items not yet opened are omitted rather than emitted as failures.

The top-level result contains `ok`, `items`, `next_request_index`, `remaining_request_count`, `truncated`, `coverage`, and optional `error`. Each page returns only the item evidence produced during that call. Items retain their original indexes; a source item split across pages repeats its index with the next non-overlapping source range, while already completed items are validated but not replayed. The caller accumulates pages until continuation is complete. When an item, aggregate source budget, result envelope, or request boundary leaves work unread, the batch returns one cursor binding the complete normalized ordered request list, aggregate cap, current request index, current per-item continuation position, and an ordered observation vector with at most one bounded entry for each completed or current request. Each entry contains the original index, observed outcome class and stable error code, and either the opaque source fingerprint for a file-backed outcome or a bounded missing/static-outcome marker. Request bodies, selector values, content, and paths are not copied into the vector; the normalized query hash already binds them. `next_request_index` names the current incomplete item or the next unopened item; `remaining_request_count` includes that item.

A continued call repeats the original ordered requests and aggregate cap with that cursor; changing them returns `cursor_mismatch`. Before returning new evidence, it re-resolves each prior observation in order. File-backed observations compare a newly opened source stamp without reparsing completed selector content, missing observations require the original target to remain missing, and static input/denial outcomes revalidate their outcome class against the query-bound request. A changed completed/current source or prior outcome returns `cursor_stale`. This bounded vector makes the changed index identifiable and keeps late-page validation proportional to at most 20 metadata observations rather than reparsing up to 19 prior files. Items after an exhausted aggregate cap are not opened and are represented only by continuation metadata.

Timeout, cancellation, or active source change returns any already completed items plus a top-level structured error and `continuation:restart`; it does not convert unopened requests into item failures or provide a cursor. A batch cursor is bounded by the common 16 KiB limit; the maximum of 20 requests is part of that public bound, not a server-side session.

### `grep`

Input: `pattern`, optional `path` (default `"."`) and `base`, optional `regex` (default true), optional `case_sensitive` (default false), `context_lines` (default 1, range 0 through 3), `limit` (default 50, range 1 through 200), optional `max_files` and `max_bytes`, and optional `cursor`. `pattern` must be non-empty UTF-8 at most 4 KiB. Scan budgets use the common defaults and maxima and must be positive. Grep has a fixed 1 MiB maximum physical source line, including its terminator when present; this is a server resource contract, not another caller option.

`grep` searches Markdown contents only. It does not perform filename ranking, semantic similarity, or graph expansion. Frontmatter, aliases, and headings are searchable because they are content. Regex mode uses Go RE2 syntax as accepted by `regexp.Compile`; invalid patterns fail before scanning. Literal mode escapes the pattern before applying the requested case behavior. An optional `ripgrep` fast path may handle only patterns proven equivalent by parity tests and must otherwise fall back to Go; it is never a required runtime dependency.

The result unit and `limit` unit are matching lines, not files or individual occurrences. Results are ordered by canonical path, one-based line, then first one-based Unicode-code-point column. The top-level result contains `ok`, canonical `path`, `matches`, `truncated`, `coverage`, and optional `error`. Each match contains `path`, `line`, `column`, `occurrences`, matching-line `text`, `before`, `after`, and the same opaque source-version `fingerprint` defined for `read`. `before` and `after` are source-ordered arrays of `{line,text}`; line terminators are omitted, overlapping context may repeat between matches, and context rows do not consume the result limit. Occurrences use Go `regexp.FindAllIndex` non-overlapping semantics, including Go's treatment of zero-width matches. Match and context text is never silently clipped. If one complete match record cannot fit even as the only result inside the absolute SDK envelope, the call returns `response_too_large` with restart coverage and no cursor rather than skipping evidence or emitting a non-advancing continuation.

Scanning is deterministic canonical-path order and stops as soon as it has enough evidence to prove a result, response, file, or byte boundary. A result/response stop returns a cursor after the last emitted matching line; a file/byte stop returns a cursor at the last fully processed source position and may validly return no matches. All deterministic partials set `scope_complete:false`. Continuation repeats the identical normalized query and budgets, revalidates the already examined catalog prefix from canonical paths plus current source stamps, and resumes without hidden server state. Metadata-only prefix validation is charged to the common two-second operation timeout and reported as `source_entries_validated`; it does not consume the new page's content `max_files` or `max_bytes` budget. A source-prefix change returns `cursor_stale`; mutation observed during a call returns `source_changed` with `continuation:restart`. If an examined physical line exceeds 1 MiB, grep returns `input_too_large` with `continuation:restart` and no cursor after reading at most a one-byte sentinel beyond the cap and before whole-line UTF-8 validation or regexp evaluation. If the caller's lower byte budget cannot process one complete source line below that fixed cap at the continuation boundary, the tool returns `limit_exceeded` rather than a non-advancing cursor; the caller must restart with a larger budget.

The grep cursor binds the normalized query and budgets, canonical stored-spelling scan boundary, next byte/line position when a file is partial, the partial file's source fingerprint, and a cumulative digest of the already examined canonical Markdown catalog prefix. The digest includes each fully examined path and its opaque source fingerprint. On continuation the server re-enumerates that metadata prefix and compares the digest, then validates the partial file's source stamp and seeks directly to its bound byte position. An insertion, deletion, rename, replacement, or detectable content change at or before the continuation boundary therefore becomes `cursor_stale`; changes strictly after an unexamined boundary do not invalidate evidence already returned. Cursor payloads retain only the bounded digest and boundary state needed to recompute and verify that prefix, not an unbounded file list.

`files_scanned` counts allowed Markdown files whose content was opened and examined during that call; `bytes_scanned` counts actual source bytes read for the new page. `source_entries_validated` counts metadata entries revisited to validate a continuation prefix. Invalid UTF-8 in an examined Markdown file returns `invalid_utf8` with restart coverage. All content scanning and metadata validation remain bounded by the shared two-second operation timeout; validation timeout returns `timeout` with `continuation:restart` and no cursor. The default 10,000-file / 256 MiB page-work budgets still bound new content work, and the absolute encoded SDK result remains 64 KiB.

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
- `AC-OBS-005`: ChatGPT/Codex can discover and select representative tools through OpenAI Secure MCP Tunnel. Phase 2 proof covers `grep` → `read_many` → continued batch reading. A future outbound-graph activation must separately prove `links` → `traverse` → continued traversal before that expanded surface is considered settled. Proof: live Obsidian tool-name/model-use cell.
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

- `GAP-OBS-002`: The five-tool core-retrieval workflow is proven live through ChatGPT; outbound and inbound graph workflows are not yet proven.
- `GAP-OBS-003`: Root confinement, denial, read-only, cursor, and sanitized-error proof is complete for the accepted five-tool core surface; equivalent proof is not yet extended to reference and graph operations.
- `GAP-OBS-004`: Full-vault backlink and path-discovery latency, scan work, response size, and freshness trade-offs are not measured. The bounded exercise/health/marathon spike and activated grep measurements are complete.
- `GAP-OBS-006`: `links`, the scoped request-local path catalog, and outbound `traverse` are not implemented.
- `GAP-OBS-007`: The full-vault activation gate for live request-local `backlinks` and `path_between` has not been run or passed; the tools are neither implemented nor advertised.
- `GAP-OBS-008`: Descriptor-owned safe telemetry summaries are complete for the accepted five-tool core surface; summaries for links, traversal, backlinks, and path discovery are not implemented.
- `GAP-OBS-009`: The accepted five-tool summaries have local JSONL/SQLite proof plus live model-driven `ls`, `grep`, and continued `read_many` telemetry; graph-tool summaries have not been proven.
- `GAP-OBS-010`: Live request-local `backlinks` and `path_between` are not implemented for pre-activation benchmark and proof.
