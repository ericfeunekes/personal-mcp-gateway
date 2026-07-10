---
title: "ADR 2026-06-30 MCP SDK Tunnel And Runtime"
status: stable
purpose: "Record the MCP implementation, tunnel adapter, search dependency, and local supervision decisions."
covers:
  - cmd/gateway/
  - internal/mcp/
  - internal/tools/obsidian/
  - docs/gateway.md
  - docs/requirements/obsidian-filesystem-tools.md
---

# ADR 2026-06-30: MCP SDK, Tunnel, And Runtime

## Status

Accepted.

## Decision

Use the official Go MCP SDK, `github.com/modelcontextprotocol/go-sdk`, as the MCP implementation layer. The gateway backend should build one server shape and expose it through either:

- stdio, using the SDK stdio transport, for local smoke tests and a possible tunnel-client command profile.
- loopback HTTP, using the SDK Streamable HTTP handler, for the OpenAI Secure MCP Tunnel HTTP profile.

Keep direct MCP JSON-RPC implementation as a contingency only if a proven OpenAI Secure MCP Tunnel or ChatGPT connector requirement cannot be satisfied through the SDK.

For Obsidian search:

- `search` starts as filename/path/title-oriented navigation inside the `obsidian` MCP server.
- `grep` owns content search inside the `obsidian` MCP server.
- `ripgrep` is an optional fast path when present, not a required global install.
- A bounded Go fallback must preserve correctness and testability.

For local process lifecycle:

- Run foreground during implementation and tunnel proof.
- Use `launchd` only after gateway health/readiness, tunnel health, idle resource use, and restart behavior are measured.

## Evidence

- OpenAI Secure MCP Tunnel docs say `tunnel-client` keeps the private MCP server inside the local boundary, opens outbound HTTPS to OpenAI, and forwards MCP requests to a local stdio command or HTTP MCP server URL: https://developers.openai.com/api/docs/guides/secure-mcp-tunnels
- OpenAI MCP and Connectors docs say remote MCP servers used by OpenAI support Streamable HTTP or HTTP/SSE transport, and tool definitions are imported through MCP tool listing: https://developers.openai.com/api/docs/guides/tools-connectors-mcp
- The Go module check resolved `github.com/modelcontextprotocol/go-sdk v1.6.1` as latest on 2026-06-30 and downloaded source from the official repository tag: https://github.com/modelcontextprotocol/go-sdk
- SDK source confirms `mcp.StdioTransport`, `mcp.NewStreamableHTTPHandler`, stateless Streamable HTTP options, and tool-name validation that allows letters, digits, `_`, `-`, and `.` up to 128 characters.
- Targeted upstream SDK tests passed locally:

```bash
go test ./mcp -run 'TestAddToolNameValidation|TestStreamableStateless'
```

Run from the downloaded `github.com/modelcontextprotocol/go-sdk@v1.6.1` module, this passed and proves the SDK-level tool registration and stateless Streamable HTTP assumptions.

## Consequences

- Do not spend first-phase effort on a custom MCP protocol implementation.
- Keep `internal/mcp/` thin and SDK-oriented.
- Keep the backend transport-independent so stdio and HTTP do not drift.
- Treat ChatGPT connector behavior for the `obsidian` server and its tool names as a live connector smoke-test requirement, not an SDK blocker.
- Avoid background indexing and required global dependencies until real-vault measurements justify them.
- Do not enable always-on startup until idle machine impact has been measured.
