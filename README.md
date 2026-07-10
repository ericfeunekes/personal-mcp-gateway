# Personal MCP Gateway

Local Go MCP gateway for exposing selected personal systems to ChatGPT through OpenAI's Secure MCP Tunnel.

Initial target integrations:

- YNAB
- Obsidian
- Voicenotes

## Intent

Run personal MCP tools locally, keep the origin private, and publish access to ChatGPT only through OpenAI's outbound Secure MCP Tunnel. The gateway should make personal data useful to external AI tools without creating a broad public API or a generic filesystem proxy.

The first implementation target is an MCP server named `obsidian` with filesystem-like, read-only tools over the local vault:

- `ls`
- `read`
- `grep`
- `search`
- `stat`
- `resolve`

Tool calls should be stateless. They may accept path-like arguments and an explicit `base`, but server-side current-directory state should not be required for correctness.

## Current Implementation

The first runnable Go slice is implemented:

- `resolve`
- shallow `ls`
- stdio mode
- loopback HTTP mode with `/mcp`, `/healthz`, and `/readyz`
- SQLite-backed structured telemetry for MCP calls, HTTP requests, and gateway lifecycle events
- local request, argument, path, telemetry-event, and tool-operation budgets
- explicit MCP impact annotations that advertise `resolve` and `ls` as read-only,
  non-destructive, and closed-world; the implementation and vault confinement
  remain the enforcement boundary

Local commands:

```bash
go run ./cmd/gateway stdio --obsidian-root /absolute/path/to/vault
go run ./cmd/gateway http --obsidian-root /absolute/path/to/vault --addr 127.0.0.1:8765
go test ./...
```

Telemetry defaults to a local SQLite database under the user config directory.
Use `--telemetry-db /absolute/path/to/telemetry.sqlite` to choose the database,
`--telemetry stderr` for live JSONL debugging, or `--telemetry off` for a quiet
run. Telemetry records sanitized metadata only: known SDK-observed tool/method
names, transport, outcome, error code, latency, bounded result counts, and path
argument shape/hashes. Unknown caller-controlled tool names, HTTP methods, and
argument keys are bucketed and run-hashed rather than stored raw. Telemetry does
not store note contents or raw paths by default.

HTTP mode rejects wildcard, unspecified, public, and non-loopback bind
addresses. The `/mcp` request body is capped before SDK handling, and required
telemetry degradation makes `/readyz` fail closed in HTTP mode. OpenAI Secure
MCP Tunnel connectivity, ChatGPT app installation, and live `tools/list`
metadata refresh are proven. A bounded model-driven ChatGPT `ls` call is also
proven through sanitized local telemetry; model-driven `resolve` remains a
proof gate.

## Operating Principles

- OpenAI Secure MCP Tunnel is the external transport boundary for ChatGPT access.
- Local services should not listen on a public interface.
- Read-only tools are the default until write paths are explicitly designed.
- Each integration should have its own MCP server name and narrow capabilities instead of generic file or API access.
- Secrets stay local and out of the repo.
- Logs should prove what was accessed without storing sensitive content by default.
- Reliability and low machine impact outrank breadth of features.

## Remaining Design And Proof Gaps

- Tool compatibility: ChatGPT accepts the `obsidian` server and displays the
  simple `ls` and `resolve` actions as read-only, and model-driven `ls`
  execution is proven; `resolve` still needs live execution proof.
- Auth mapping: how OpenAI connector identity maps to allowed MCP capabilities.
- Deployment: foreground first; `launchd` after tunnel proof, resource limits, health checks, and idle impact measurements.
- Telemetry operations: retention, compaction, and optional encryption are not designed yet.

## Docs

- `docs/ARCHITECTURE.md` records the gateway shape and domain boundaries.
- `docs/obsidian.md` owns the Obsidian MCP server contract.
- `docs/requirements/obsidian-filesystem-tools.md` defines the first requirements slice.
- `docs/TESTING.md` defines proof expectations for reliability, robustness, and minimal machine impact.
