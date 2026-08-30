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

The `obsidian` MCP server exposes narrow agent tools over one configured local vault. The accepted implementation combines read-only discovery and Markdown retrieval with the separately governed mutation surface and activated native PDF reading; additional native-document representations remain activation-gated. Correctness must not depend on hidden server-side state.

## Tool Vocabulary

Target tool names:

- `ls`
- `resolve`
- `read`
- `read_many`
- `grep`
- `stat`
- `links`
- `traverse`
- `backlinks`
- `path_between`
- `write`
- `edit`
- `move`
- `delete`
- `read_document` (PDF activated; additional representations activation-gated)

The MCP server name is the public integration boundary. Do not prefix tool names with `obsidian.` inside this server, and do not add non-Obsidian tools to this server. Do not expose separate `search`, `graph_search`, shell, or generic query tools: `grep` is the content-discovery entry point, `resolve` owns canonical path resolution and existence, and mutation-scoped `stat` owns opaque fingerprint acquisition for existing files and directories.

The target list is phased. `tools/list` advertises only fully implemented and proven tools, never disabled placeholders. `backlinks` and `path_between` remain absent until the numeric full-vault activation gate in the requirements passes.

The mutation phase adds `stat`, `write`, `edit`, `move`, and `delete` together after its filesystem foundation is proven. `stat` is a narrow read-only prerequisite for mutation-ready file and directory identity, not a general replacement for `resolve`. The four mutation tools are advertised by default on the trusted personal connector with no separate server-side write gate. They use exact fingerprint, absence, and patch-context preconditions with last-available pre-effect revalidation, atomic whole-file visibility and absent-destination no-replace where supported, and an explicitly accepted macOS external-writer window after final revalidation. Their destructive annotations, permanent-delete semantics, recovery boundary, fixed limits, and non-CAS concurrency contract live in `requirements/obsidian-mutation-tools.md`.

Implemented and accepted core tools:

- `resolve`: return stored-spelling/NFC identity and metadata for explicit `path` relative to optional `base`, including successful `exists:false` results for missing paths; model-visible paths use the same base-relative coordinate.
- `ls`: list one directory level in canonical order with hidden-entry filtering, symlink non-traversal, a maximum limit of 500 entries, stateless source/query-bound cursors, truthful coverage, and a 64 KiB SDK-result cap.
- `read`: select bounded content, heading, block, frontmatter, or outline evidence from one Markdown path relative to optional `base`, with source-bound continuation.
- `read_many`: preserve one to 20 ordered read requests relative to one optional top-level `base` under one aggregate byte budget, isolate item errors, and continue with a request-vector-bound cursor.
- `grep`: search Markdown content in deterministic canonical-path order with bounded context, explicit work budgets, truthful coverage, and stateless continuation.
- `read_document`: accepted PDF support that captures one confined, validated source up to 7,000,000 bytes and returns its original bytes as `application/pdf`. Unsupported document representations fail closed and remain absent from the support claim.
- `stat`: return base-relative safe metadata and an opaque mutation fingerprint for one allowed regular file or empty directory.
- `write`: create an absent file or atomically replace one complete file value under an exact precondition and the 512 KiB decoded-value cap.
- `edit`: validate and atomically apply one 1-to-64-operation exact-context patch against a fingerprinted source.
- `move`: exclusively rename one fingerprinted file or empty directory to an explicitly absent destination.
- `delete`: permanently remove one fingerprinted file or empty directory without trash, undo, or recovery state.

The local implementation derives registration, schemas, backend-ready names, and safe telemetry from one descriptor authority. Filesystem access is fd-anchored per operation, pagination re-scans the complete shallow directory while retaining only bounded candidates or cursor state, and JSONL/SQLite summaries cannot retain raw paths, entry names, patterns, selectors, cursor values, or content. Phase 1 proof covers the original `resolve`/`ls` boundary. Phase 2 proof is intentionally layered: synthetic fixtures cover retrieval semantics; current-vault probes cover broad `grep`, inventory, performance, and resources; and the authenticated model journey covers live `grep` -> `read_many` -> continued `read_many`. Together with five-tool metadata and exact release acceptance, those layers prove the accepted core surface without claiming every retrieval selector was exercised against the real vault.

The 64 KiB encoded SDK result limit is the absolute context envelope for every Obsidian tool. Phase 2 retrieval may accept source/content work budgets up to 256 KiB, but it must page any larger selected work beneath that envelope with caller-carried cursors. Single-note Markdown parsing is capped at 8 MiB and 50,000 physical source lines so source-unit selection remains memory-bounded without another agent-facing option. `grep` keeps the 1 MiB materialization cap for regular expressions, while literal mode streams longer physical lines without retaining them whole and returns explicit bounded excerpts when line evidence would otherwise dominate the SDK envelope. Retrieval uses one shared coverage grammar; `grep` favors useful early pages and reports incomplete scope rather than continuing an expensive scan only to strengthen a completeness claim.

Not implemented yet: `links`, `traverse`, `backlinks`, and `path_between`.
The private vault-confined mutation foundation, public five-tool mutation
delta, and authenticated installed-connector journey are implemented and
proven. Issue #12 owns the remaining non-PDF native-document representations.

## Stateless Path Model

Tools should accept explicit path context:

```json
{ "path": "home/projects", "base": "", "limit": 100 }
```

`base` gives ordinary working-directory ergonomics, but it is an input, not server session state. `path` is resolved relative to it, `.` and `..` behave normally, and the final normalized target must remain inside the vault. Model-visible path fields are expressed relative to that same base, including `../` when a result lies in another vault folder, so a returned path can be reused unchanged with the same base. With no base, paths are vault-relative. Server-side `cd` state is not part of the design.

The MCP does not require a setup read, discover or auto-read `AGENTS.md`, or interpret project instructions. A ChatGPT project may tell the model which base to use and which instruction file to read; those directions remain ordinary model instructions composed from the existing tools.

The filesystem core retains canonical stored-spelling/NFC vault identities for ordering, fingerprints, source validation, and cursor state. The Obsidian tool boundary converts only model-visible paths into the caller's coordinate system before response-size fitting.

## Vault Boundary

All tool targets are vault-relative after combining `base` and `path`. The filesystem adapter must reject absolute tool inputs, normalized traversal outside the vault, symlink escapes, hidden local databases, secret directories, and any file outside the configured vault root. Ordinary `..` segments that remain inside the vault are allowed. Only process startup config may supply the absolute vault root. Content and reference tools operate on Markdown files; `ls` and `resolve` may still report safe attachment metadata. Native document reading is governed by `requirements/obsidian-document-reading.md`; write, edit, move, and permanent delete are governed by `requirements/obsidian-mutation-tools.md`.

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

- `GAP-OBS-002`: The five-tool core-retrieval workflow is proven live through ChatGPT; outbound and inbound graph workflows are not yet proven.
- `GAP-OBS-003`: Root confinement, denial, read-only, cursor, and sanitized-error proof is complete for the accepted five-tool core surface; equivalent proof is not yet extended to reference and graph operations.
- `GAP-OBS-004`: Full-vault backlink and path-discovery latency, scan work, response size, and freshness trade-offs are not measured. The bounded exercise/health/marathon spike and activated grep measurements are complete.
- `GAP-OBS-006`: `links`, the scoped request-local path catalog, and outbound `traverse` are not implemented.
- `GAP-OBS-007`: The full-vault activation gate for live request-local `backlinks` and `path_between` has not been run or passed; the tools are neither implemented nor advertised.
- `GAP-OBS-008`: Descriptor-owned safe telemetry summaries are complete for the accepted five-tool core surface; summaries for links, traversal, backlinks, and path discovery are not implemented.
- `GAP-OBS-009`: The accepted five-tool summaries have local JSONL/SQLite proof plus live model-driven `ls`, `grep`, and continued `read_many` telemetry; graph-tool summaries have not been proven.
- `GAP-OBS-010`: Live request-local `backlinks` and `path_between` are not implemented for pre-activation benchmark and proof.
