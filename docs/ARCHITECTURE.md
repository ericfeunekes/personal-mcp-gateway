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

This repo will build a local Go MCP gateway backend that can be started in different transport modes. It will expose narrow MCP tools to ChatGPT through OpenAI's Secure MCP Tunnel, starting with an MCP server named `obsidian` for read-only filesystem-like tools over the local Obsidian vault.

The chosen public namespace shape is the MCP server name, not dotted tool names. The Obsidian server exposes simple tool names such as `ls` and `resolve`; future integrations should be exposed as separate MCP server entries such as `ynab` or `voicenotes`, not as dotted tools in one combined server.

## System Shape

- `cmd/gateway/` — process entrypoint that selects stdio or HTTP mode.
- `internal/app/` — transport-independent backend module that owns config, tool registry construction, shared limits, and lifecycle wiring.
- `internal/mcp/` — thin adapters around the official Go MCP SDK for server construction, transport selection, tool registration, and protocol response handling.
- `internal/tools/obsidian/` — Obsidian tool handlers and tool schemas.
- `internal/fsx/` — root-confined filesystem adapter for vault traversal, path normalization, bounded reads, and search helpers.
- `internal/limits/` — shared resource budgets for protocol payloads, telemetry summaries, path inputs, and tool operation timeouts.
- `internal/config/` — local config loading without secrets in repo.
- `internal/audit/` — metadata-only SQLite/JSONL access logs and operational events.

The initial deployable unit is the Obsidian MCP server process. The same backend module is started in either stdio mode for local smoke tests or HTTP mode for OpenAI tunnel integration. Do not build separate stdio and HTTP server implementations. Future integrations may share internal gateway code, but they should become separately named MCP server entries before they become model-visible.

Use `github.com/modelcontextprotocol/go-sdk` as the MCP implementation layer. The backend should construct one MCP server shape and expose it through `mcp.StdioTransport` or `mcp.NewStreamableHTTPHandler`; direct JSON-RPC protocol code belongs outside the initial design.

## Domains

- `gateway.md` owns process lifecycle, OpenAI tunnel assumptions, config, health, audit, and cross-server tool registration.
- `obsidian.md` owns vault-specific filesystem-like behavior and the `obsidian` server's tool vocabulary.

## Dependency Direction

Request handling should flow inward:

`transport adapter -> backend app -> MCP/tool dispatcher -> domain handler -> vault/filesystem adapter`

Rules:

- Transport adapters may depend on the backend app, but the backend app must not depend on a specific transport mode.
- Tool handlers may depend on `internal/fsx`, `internal/config`, `internal/audit`, and `internal/limits`.
- `internal/fsx` must not know MCP, ChatGPT, OpenAI tunnel state, or Obsidian-specific tool names.
- Domain handlers must validate MCP inputs before calling filesystem adapters.
- No package should expose raw unrestricted host filesystem access.

## State Ownership

The Obsidian vault remains the source of truth for notes and files. This gateway is a read model and transport adapter, not an authority for vault state.

Tool calls should be stateless:

- No server-side current working directory is required for correctness.
- Tools accept explicit `path`, `base`, cursors, depth, byte limits, and result limits.
- A model may carry a `base` from prior results to get shell-like ergonomics, but every call remains replayable and retry-safe.

Persistent gateway state is limited to local config, metadata-only SQLite telemetry, and later optional indexes if a measured performance need justifies them.

## Quality Attributes

- **Reliability**: tool calls must be bounded, cancellable, and safe under retry.
- **Low machine impact**: idle footprint should be small; startup must not scan the whole vault; expensive operations need limits and timeouts.
- **Security**: every file operation must be root-confined to the configured vault or domain root.
- **Extensibility**: future integrations such as `ynab` and `voicenotes` should add separately named MCP server entries without weakening Obsidian boundaries.

## Intentionally Not Generalized Yet

- No write tools.
- No generic filesystem, shell, or HTTP proxy server.
- No background indexer until live filesystem/search proof shows a need.
- No multi-user authorization model until a real non-personal consumer appears.
- No unrelated integration tools inside the `obsidian` server.
- No custom MCP protocol implementation unless the official SDK blocks a proven tunnel or ChatGPT compatibility requirement.

## Current Gaps

See `feature-gap-map.md`.
