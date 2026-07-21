---
title: "Architecture"
status: draft
purpose: "Define the gateway shape, dependency directions, MCP server naming model, and first implementation boundaries."
covers:
  - cmd/
  - internal/
  - docs/gateway.md
  - docs/obsidian.md
---

# Architecture

## Current Answer

This repo builds a local Go MCP gateway backend that can be started in different transport modes. It exposes narrow MCP tools to ChatGPT through OpenAI's Secure MCP Tunnel, starting with an MCP server named `obsidian` for discovery, bounded reading, authored-reference traversal, and explicitly scoped vault mutation over the local Obsidian vault.

The chosen public namespace shape is the MCP server name, not dotted tool names. The Obsidian server exposes simple tool names such as `ls` and `resolve`; future integrations should be exposed as separate MCP server entries such as `ynab` or `voicenotes`, not as dotted tools in one combined server.

## System Shape

- `cmd/gateway/` — process entrypoint that selects stdio or HTTP mode.
- `cmd/release-activation/` — private command adapter for the local release
  lifecycle; it maps arguments and bounded records but does not decide or
  persist transitions.
- `internal/app/` — transport-independent backend module that owns config, tool registry construction, shared limits, and lifecycle wiring.
- `internal/releaseactivation/` — the sole authority for the local
  prepared/pending/accept/rollback state machine, transaction persistence,
  target replacement/recovery, and lock-held supervisor/source-update effects.
- `internal/mcp/` — thin adapters around the official Go MCP SDK for server construction, transport selection, tool registration, and protocol response handling.
- `internal/tools/obsidian/` — Obsidian tool handlers, schemas, and ownership of Obsidian-specific note/reference semantics; implementation planning may split internal child packages without moving those semantics into `fsx`.
- `internal/fsx/` — root-confined filesystem adapter for vault traversal, path normalization, bounded reads, and search helpers.
- `internal/limits/` — shared resource budgets for protocol payloads, telemetry summaries, path inputs, and tool operation timeouts.
- `internal/config/` — local config loading without secrets in repo.
- `internal/audit/` — metadata-only SQLite/JSONL access logs and operational events.

The initial deployable unit is the Obsidian MCP server process. The same backend module is started in either stdio mode for local smoke tests or HTTP mode for OpenAI tunnel integration. Do not build separate stdio and HTTP server implementations. Future integrations may share internal gateway code, but they should become separately named MCP server entries before they become model-visible.

Use `github.com/modelcontextprotocol/go-sdk` as the MCP implementation layer. The backend should construct one MCP server shape and expose it through `mcp.StdioTransport` or `mcp.NewStreamableHTTPHandler`; direct JSON-RPC protocol code belongs outside the initial design.

## Domains

- `gateway.md` owns process lifecycle, OpenAI tunnel assumptions, config, health, audit, and cross-server tool registration.
- `obsidian.md` owns vault-specific read/reference behavior and the `obsidian` server's tool vocabulary.

## Dependency Direction

Request handling should flow inward:

`transport adapter -> backend app -> MCP/tool dispatcher -> domain handler -> vault/filesystem adapter`

Rules:

- Transport adapters may depend on the backend app, but the backend app must not depend on a specific transport mode.
- Tool handlers may depend on `internal/fsx`, `internal/config`, `internal/audit`, and `internal/limits`.
- `internal/fsx` must not know MCP, ChatGPT, OpenAI tunnel state, Obsidian-specific tool names, wikilinks, aliases, headings, blocks, or graph semantics.
- Domain handlers must validate MCP inputs before calling filesystem adapters.
- No package should expose raw unrestricted host filesystem access.

## Shared Obsidian Tool Substrate Ownership

The expanded read and graph tools share identity, bounded I/O, continuation, coverage, response budgeting, and telemetry behavior. They must reuse one ownership split rather than reimplementing these concerns per tool:

- `internal/fsx` owns stored-spelling/NFC canonical vault-path identity, root confinement, safe generic file opens/reads/walks, cancellation checks, and generic files/bytes work accounting.
- `internal/tools/obsidian` owns selectors, Markdown/reference semantics, catalog and graph meaning, cursor payloads and query binding, source-fingerprint interpretation, completeness claims, deterministic continuation order, and structured response-byte budgeting. Child packages may separate pure parsing or cursor logic, but ownership remains in the Obsidian domain.
- `internal/limits` owns only budgets that are genuinely shared across the process or transport. Public limits specific to one Obsidian tool remain part of the Obsidian domain contract.
- Each Obsidian tool descriptor is the single authority for its public name, SDK schema/handler, annotations, and safe argument/result summarizers. The app composes activated descriptor groups and derives both SDK registration and telemetry's known-tool set from that same collection; separate tool-name, registration, or summarizer registries are forbidden.
- `internal/mcp` owns only the generic descriptor/telemetry-summary handoff contract. MCP middleware must not infer every Obsidian schema itself or import the Obsidian package.
- `internal/audit` owns bounded event persistence and degradation behavior; it does not know note, selector, link, graph, or cursor semantics.

`resolve` returns canonical identity and metadata but does not invent scan coverage. Scan and graph tools report coverage for the work they actually perform. Result-size enforcement happens before the domain returns structured content to the SDK; transport input limits are not output limits.

## State Ownership

The Obsidian vault remains the source of truth for notes and files. This gateway is a read model and transport adapter, not an authority for vault state.

Tool calls should be stateless:

- No server-side current working directory is required for correctness.
- Tools accept explicit `path`, `base`, cursors, depth, byte limits, and result limits.
- A model may carry a `base` from prior results to get shell-like ergonomics, but every call remains replayable and retry-safe.

Persistent gateway runtime state is limited to local config, metadata-only
SQLite telemetry, and later optional indexes if a measured performance need
justifies them.

The local release transaction is a deliberate state-ownership exception. It is
deployment state, not MCP or vault state, and has one fixed, non-configurable
per-user slot derived from the effective user's passwd home. A permanent lock
and an optional `active/` transaction retain a versioned manifest, immutable
candidate, optional previous binary, and the pinned controller that created the
transaction. The transaction never stores note content, vault paths, or tunnel
credentials, and operators must not edit it as a recovery interface.

`internal/releaseactivation` alone may interpret or mutate that state. Public
Make targets and the stable dispatcher select the current controller when the
slot is clear or the transaction's pinned controller when it is active; after
locking, the authority revalidates its identity and all installed/artifact
facts. Shell wrappers and private host-effect adapters do not parse manifests,
choose transitions, or maintain fallback rollback state.

The dispatcher performs optimistic selection only. It invokes the selected
controller through separate private `0600` stdout/stderr files with a 64 KiB
cap, suppresses child-start diagnostics, and retries at most once only when the
controller returns the byte-exact pre-effect authority-mismatch record. A child
start failure, any other error record, and any result after an effect are final.
The private controller's release-or-guide operation makes the one retry safe:
clear prepares then resumes, prepared resumes, and later active states return
bounded legal recovery guidance without mutation.

Source fetch may occur outside the lifecycle lock because it does not mutate the
checkout. Final clear-state, branch, tree, HEAD, and fetched-ref validation plus
the fast-forward occur while holding the same lock used by release preparation.
Repo-owned restart, LaunchAgent install, and LaunchAgent uninstall are also
clear-only, lock-held administrative effects. The adapters that invoke
`launchctl` remain private implementation details.

## Quality Attributes

- **Reliability**: tool calls must be bounded, cancellable, and safe under retry.
- **Low machine impact**: idle footprint should be small; startup must not scan the whole vault; expensive operations need limits and timeouts.
- **Security**: every file operation must be root-confined to the configured vault or domain root.
- **Extensibility**: future integrations such as `ynab` and `voicenotes` should add separately named MCP server entries without weakening Obsidian boundaries.

## Intentionally Not Generalized Yet

- No generic filesystem mutation surface: mutation behavior is restricted to the named Obsidian operations defined by `requirements/obsidian-mutation-tools.md`.
- No generic file-serving surface: native document delivery is restricted to the activation-gated behavior in `requirements/obsidian-document-reading.md`.
- No generic filesystem, shell, or HTTP proxy server.
- No background indexer until full-vault scan and graph proof shows a need.
- No multi-user authorization model until a real non-personal consumer appears.
- No unrelated integration tools inside the `obsidian` server.
- No custom MCP protocol implementation unless the official SDK blocks a proven tunnel or ChatGPT compatibility requirement.

## Current Gaps

See `feature-gap-map.md`.
