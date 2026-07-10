---
title: "ADR 2026-06-30 Go And Dotted Tool Names"
status: superseded
purpose: "Record the language and public namespace decision for the personal MCP gateway."
covers:
  - cmd/
  - internal/
  - docs/ARCHITECTURE.md
---

# ADR 2026-06-30: Go And Dotted Tool Names

## Status

Superseded by `ADR-2026-07-02-obsidian-server-tool-names.md`.

## Decision

Build the gateway core in Go as one transport-independent backend module, start that backend in stdio or HTTP mode, and use dotted MCP tool names as the public namespace contract, starting with `obsidian.*`.

Examples:

- `obsidian.ls`
- `obsidian.read`
- `obsidian.grep`
- `obsidian.search`
- `obsidian.stat`
- `obsidian.resolve`

## Context

The gateway will run locally on a personal machine, expose personal data surfaces through OpenAI's Secure MCP Tunnel, and needs high reliability with minimal machine impact. The first namespace is Obsidian, with filesystem-like read-only tools over a relatively large vault. Future namespaces may include YNAB, Voicenotes, or other explicit systems.

## Rationale

Go gives this project a small always-on runtime, straightforward filesystem performance, simple deployment as a single binary, and good built-in support for HTTP servers, request cancellation, timeouts, and concurrency limits.

Dotted tool names make ownership visible in the MCP surface without requiring separate processes or generic tool names. They also keep future namespaces from colliding with Obsidian verbs.

## Consequences

- The first implementation should create a Go module with a shared backend under `internal/app/` and namespace modules under `internal/tools/`.
- Stdio and HTTP startup modes should be thin adapters over the shared backend, not separate implementations.
- Tool calls remain stateless; shell-like navigation is represented by explicit `path` and `base` arguments.
- ChatGPT compatibility with dotted tool names must be smoke-tested before treating the API as final.
- Python, TypeScript, and Rust remain options for future helper tools, but not for the gateway core unless this ADR is superseded.

## Follow-On Decision

Use the official Go MCP SDK for the MCP implementation layer. Keep direct protocol implementation as a fallback only if the SDK cannot satisfy a proven OpenAI Secure MCP Tunnel or ChatGPT connector requirement.
