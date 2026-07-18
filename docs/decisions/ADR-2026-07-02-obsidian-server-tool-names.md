---
title: "ADR 2026-07-02 Obsidian Server Tool Names"
status: partially_superseded
purpose: "Record the MCP server naming and historical tool vocabulary decision after local Codex dogfooding."
covers:
  - cmd/
  - internal/
  - docs/ARCHITECTURE.md
  - docs/obsidian.md
---

# ADR 2026-07-02: Obsidian Server Tool Names

## Status

Accepted for server-name ownership and simple tool naming. Supersedes
`ADR-2026-06-30-go-dotted-tool-names.md`. The example vocabulary below was
superseded by `ADR-2026-07-10-obsidian-agent-tool-surface.md`.

## Decision

Expose the first integration as an MCP server named `obsidian`. Inside that
server, use simple tool names. The historical target list was:

- `ls`
- `read`
- `grep`
- `search`
- `stat`
- `resolve`

The first implemented slice registers only `ls` and `resolve`. The current
target vocabulary is defined in
`ADR-2026-07-10-obsidian-agent-tool-surface.md`.

Future integrations should use their own MCP server names, for example `ynab`
or `voicenotes`, rather than adding dotted tool names to the Obsidian server.

## Context

Initial planning chose dotted tool names such as `obsidian.ls` because the
gateway was imagined as one multi-integration MCP server. Local Codex dogfooding
showed that the client already introduces a server-level namespace for external
MCP tools. Keeping both the server name and an `obsidian.` tool prefix creates a
redundant model-facing surface.

## Rationale

Server-name ownership keeps the model-facing API shorter while preserving a
clear boundary between personal systems. It also makes future integrations
easier to reason about operationally: each connector or Codex MCP entry can be
named for the system it exposes.

Simple tool names are acceptable because the MCP server already provides the
`obsidian` namespace. The server must not add unrelated YNAB, Voicenotes,
shell, generic filesystem, or generic HTTP proxy tools.

## Consequences

- The MCP server implementation reports `obsidian` as its server name.
- Obsidian tool registration uses `ls` and `resolve`, not `obsidian.ls` and
  `obsidian.resolve`.
- Telemetry records simple tool names in the indexed `tool` column and records
  the server name in lifecycle metadata.
- ChatGPT/OpenAI Secure MCP Tunnel proof should validate the `obsidian` server
  with simple tool names.
- The Go module, repo name, and binary may remain gateway-oriented implementation
  details unless deployment pressure justifies renaming them.
