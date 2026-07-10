---
title: "Obsidian Filesystem Tools Requirements"
status: draft
purpose: "Define the first implementation slice for read-only Obsidian filesystem-like MCP tools."
covers:
  - internal/tools/obsidian/
  - internal/fsx/
  - docs/obsidian.md
---

# Obsidian Filesystem Tools Requirements

## Desired Outcome

ChatGPT can inspect the local Obsidian vault through narrow read-only MCP tools exposed by the local Go gateway. The interaction should feel filesystem-like enough to browse and search notes, while every tool call remains stateless, bounded, root-confined, and safe to retry.

## Settled Decisions

- Implementation language: Go.
- Runtime shape: one transport-independent backend module started in stdio or HTTP mode.
- Public namespace: MCP server name `obsidian`; tools inside the server use simple names such as `ls`.
- First integration server: Obsidian.
- Exposure route: OpenAI Secure MCP Tunnel, not Cloudflare.
- State model: explicit `path` and `base` arguments, no required server-side current directory.
- First phase: read-only only.
- MCP implementation: official Go MCP SDK first; direct MCP implementation only as a fallback.
- Search launch shape: `search` is filename/path/title-oriented at launch; `grep` owns bounded content search.
- Runtime search dependency: `ripgrep` is an optional fast path when present, with a bounded Go fallback for correctness.
- Local supervision: run foreground during implementation and tunnel proof; target `launchd` after idle impact and health/readiness are proven.

## In Scope

- `ls`: list vault-relative directory entries with depth and result limits. The first shipped slice is shallow only: one directory level, deterministic order, default limit 100, maximum limit 500.
- `read`: read bounded text from an allowed vault file.
- `grep`: search file contents under an explicit path/base with limits and timeout.
- `search`: find likely note/file matches by filename, path, title, or later content strategy.
- `stat`: return metadata for a vault-relative path without content.
- `resolve`: normalize a path against an optional base and return a canonical vault-relative path or a denial.

## Implemented First Slice

- Runnable Go gateway process under `cmd/gateway/`.
- Shared backend exposed through stdio or loopback HTTP.
- `resolve`.
- Shallow `ls`.
- Runtime `--obsidian-root` config with absolute-root validation.
- Vault-relative tool input validation with absolute path, traversal, hidden segment, and symlink traversal denial.
- Local `/healthz`, `/readyz`, and `/mcp` endpoints in HTTP mode.
- Metadata-only telemetry with sanitized tool-call, MCP request, HTTP request,
  gateway lifecycle, and telemetry degradation events.

## Out Of Scope

- Write, edit, delete, move, rename, or create tools.
- Generic host filesystem access.
- Shell commands exposed to ChatGPT.
- Server-side `cd` or session state required for correctness.
- Background vault indexing as a first-phase requirement.
- Committing vault paths, note contents, tokens, tunnel credentials, or local database paths.

## Source Of Truth

The Obsidian vault is the source of truth. The gateway is a read-only adapter and may only project bounded views of vault files. Any future cache or index is a derived projection and must have invalidation and rebuild behavior documented before it becomes authoritative for responses.

## Dependencies

- The gateway should use `github.com/modelcontextprotocol/go-sdk` for MCP protocol handling, stdio, and Streamable HTTP.
- The OpenAI Secure MCP Tunnel setup must be able to reach the local gateway process.
- ChatGPT must accept an `obsidian` MCP server with simple tool names.
- `ripgrep` may improve `grep` performance when available, but no first-phase tool should require a global install.

## Acceptance Criteria

- `AC-OBS-001`: Each advertised tool is registered under the `obsidian` MCP server with its simple tool name and rejects calls without the required explicit path/base inputs where applicable. Proof: MCP tool-list and tool-call boundary tests per `docs/TESTING.md`.
- `AC-OBS-002`: Path normalization confines all operations to the configured vault root and rejects traversal, symlink escapes, and unconfigured absolute paths. Proof: filesystem adapter boundary tests and fixture symlink/traversal cases per `docs/TESTING.md`.
- `AC-OBS-003`: Read and search operations are bounded by byte, result, depth, and time limits, and cancellation stops work promptly. Proof: timeout/cancellation tests and large-fixture tests per `docs/TESTING.md`.
- `AC-OBS-004`: No first-phase tool mutates the vault. Proof: integration test against a temporary fixture vault plus before/after filesystem snapshot per `docs/TESTING.md`.
- `AC-OBS-005`: ChatGPT connector behavior is smoke-tested with the `obsidian` MCP server and the OpenAI Secure MCP Tunnel before the API is considered settled. Proof: manual live-service verification recorded in closeout notes per `docs/TESTING.md`.
- `AC-OBS-006`: Idle gateway impact is measured and documented before always-on local use. Proof: local process observation for idle CPU, memory, and file descriptors per `docs/TESTING.md`.
- `AC-OBS-007`: `search` does not scan file contents at launch except through explicitly bounded search behavior added after measurement. Proof: tests showing filename/path/title matching stays separate from `grep`.

## Deferred Telemetry Decisions

- First-slice audit metadata is defined in `docs/gateway.md`: canonical known
  tool names and SDK-observed method names, bounded outcomes, error codes,
  latency, result counts, sanitized identifier classes/hashes, and path
  shape/hashes without note content or raw paths.
- Future `read`, `grep`, `search`, and
  `stat` must add their own safe per-tool telemetry summaries before
  implementation.
- Retention, compaction, and optional encryption for long-running telemetry
  storage remain operations decisions.

## Current Gaps

- `GAP-OBS-001`: `read`, `grep`, `search`, and `stat` schemas and handlers are not implemented.
- `GAP-OBS-002`: ChatGPT app installation, simple tool-name discovery, and
  read-only action classification are proven. A bounded model-driven `ls` call
  is proven; model-driven `resolve` is not.
- `GAP-OBS-003`: Root confinement and path-denial proof must be extended when future read, search, grep, and stat tools are implemented.
- `GAP-OBS-004`: Real-vault performance and idle machine impact are not measured.
- `GAP-OBS-005`: OpenAI tunnel foreground and LaunchAgent startup, ChatGPT app
  installation, live metadata refresh, and a bounded ChatGPT `ls` call are
  proven for the stdio profile; model-driven `resolve` and idle-impact
  readiness are not proven.
