---
title: "Gateway Domain"
status: draft
purpose: "Own the local MCP gateway process, OpenAI tunnel boundary, config, health, and cross-server rules."
covers:
  - cmd/gateway/
  - cmd/release-activation/
  - internal/mcp/
  - internal/config/
  - internal/audit/
  - internal/releaseactivation/
---

# Gateway Domain

The gateway is a local Go backend that exposes selected personal-system tools through MCP. The same backend module should be started in stdio mode for local smoke tests or HTTP mode for OpenAI Secure MCP Tunnel integration. In HTTP mode it should listen only on a local interface and should not require inbound network access.

The gateway should use the official Go MCP SDK, `github.com/modelcontextprotocol/go-sdk`, rather than directly implementing MCP JSON-RPC. As of the 2026-06-30 spike, `v1.6.1` is the latest released module, declares Go 1.25, provides `mcp.StdioTransport`, and provides `mcp.NewStreamableHTTPHandler` with stateless Streamable HTTP support. Direct protocol implementation is a contingency only if a future OpenAI tunnel or ChatGPT connector requirement cannot be met through the SDK.

## Responsibilities

- Register integration-owned tool names such as `ls` inside the `obsidian` MCP server.
- Serve MCP requests over the transport selected for the OpenAI tunnel.
- Use the Go MCP SDK for MCP protocol, stdio transport, and Streamable HTTP handling.
- Keep stdio and HTTP startup modes as adapters over one backend module, not separate implementations.
- Enforce cross-cutting request limits, cancellation, timeouts, and structured errors.
- Load local config without committing secrets, vault paths, tokens, or tunnel credentials.
- Provide health and readiness signals for local supervision.
- Emit metadata-only audit records that identify tool, sanitized path argument shape, timing, bounded result size, and outcome without storing note contents or raw paths by default.

## Non-Responsibilities

- The gateway is not a generic filesystem proxy.
- The gateway is not a shell execution surface.
- The gateway is not the source of truth for vault contents or personal-system data.
- The gateway should not run background scans or indexers unless an explicit later requirement adds them.

## Server Naming Rules

Integration ownership is represented by the MCP server name:

- The Obsidian server is named `obsidian` and exposes simple tools such as `ls` and `resolve`.
- Future integrations should use their own MCP server entries, for example `ynab` or `voicenotes`.

Domain modules own their tool schemas, input validation, and safe per-tool
telemetry summaries. The gateway owns registration, protocol mapping, health,
resource limits, and audit plumbing. Do not mix unrelated integration tools into
the `obsidian` server to simulate namespacing.

Generic MCP middleware receives domain-owned tool descriptors through app composition. Each descriptor is the single source for tool name, registration/schema/handler, annotations, and safe summaries; the app derives its registered and known-tool sets from the activated descriptors rather than parallel lists or maps. Middleware may record bounded safe counters, but it must not import an integration package, inspect integration-specific content, or grow a central switch for every future tool field. Domain summaries never include raw paths, patterns, selectors, cursors, link text, snippets, note content, or candidate names.

## OpenAI Docs

This repo carries a project-scoped OpenAI docs MCP config in `.codex/config.toml`. Use it before implementing tunnel, Apps SDK, connector, or MCP assumptions. If the running Codex session does not expose `openaiDeveloperDocs`, restart Codex from this repo.

Current OpenAI Secure MCP Tunnel docs say `tunnel-client` runs inside the network that can reach the private MCP server, makes outbound HTTPS requests to OpenAI, and forwards MCP requests to either a local stdio command or an HTTP MCP server URL. For this repo, keep both startup modes:

- stdio mode for fast local smoke tests and a possible `tunnel-client --mcp-command` profile.
- HTTP mode on loopback for `tunnel-client --mcp-server-url http://127.0.0.1:<port>/mcp` after local HTTP tests pass.

Use foreground processes for implementation and short debugging sessions. The
current always-on stdio profile uses `launchd`: gateway/tunnel health, bounded
idle impact, and automatic recovery after a forced tunnel-process exit were
proven on 2026-07-10.

The repo-local foreground tunnel wrapper is `scripts/run-obsidian-tunnel.sh`.
It loads ignored local settings from `.env.local`, generates a temp
`obsidian-stdio` tunnel-client profile, and passes the tunnel runtime key by
environment reference (`env:CONTROL_PLANE_API_KEY`) so the key is not written
into the profile. See `docs/runbooks/openai-tunnel.md` for the operator flow
and latest foreground tunnel proof.

The canonical always-on local deployment is `make release`, documented in
`docs/runbooks/local-release.md`. It builds and probes the gateway binary before
atomically replacing the configured `GATEWAY_BIN`, restarts and verifies the
LaunchAgent, and then leaves the release pending authenticated metadata/model
proof. Local readiness is necessary but does not accept a candidate.

The public release contract has four commands: `make release`, diagnostic
`make release-status`, and exact-ID `make release-accept` /
`make release-rollback`. The normal fast path omits status: release, refresh
connector metadata and complete the required model-selected journey, then
accept or roll back that same full release ID. An interrupted `prepared`
transaction resumes through `make release` with the same immutable candidate.
Missing or malformed release IDs are rejected by the same controller grammar
as every other invalid release command; Make adds only its ordinary bounded
target/exit diagnostic. `make update` pins the fetched commit object before
entering the lifecycle lock and releases that lock before calling the release
script directly.

`internal/releaseactivation` is the only transition and persistence authority.
Its fixed per-user slot keeps the immutable candidate, optional previous binary,
and controller copy needed to recover independently of mutable source/build
output. `cmd/release-activation` is only the private CLI adapter, while
`scripts/release-activation.sh` is a stable selector for the current or pinned
controller. It treats any `active` directory entry, including a dangling link,
as active and fails closed unless the pinned authority is a regular executable.
Controller output crosses private, bounded channels and is relayed only after
completion; one pre-effect authority-selection race may be reselected once.
Restart and LaunchAgent install/uninstall effects use private
adapters, remain under the shared fail-fast lock, and are permitted only while
the transaction is clear.

Git synchronization is deliberately separate. `make update` fetches without
holding the lifecycle lock, then locks and revalidates a clear slot, clean
`main`, and unchanged HEAD/tree before verifying and fast-forwarding to the
immutable fetched commit ID captured before lock acquisition. It never merges
mutable `FETCH_HEAD`, and releases the lock before the updated checkout starts
the same release path.

## Telemetry

Structured telemetry is part of the first reliability surface. The default sink
is a local SQLite database at the user config path
`personal-mcp-gateway/telemetry.sqlite`, configurable with
`--telemetry-db /absolute/path/to/telemetry.sqlite`. The gateway also supports
`--telemetry stderr` for JSONL live debugging and `--telemetry off` for a quiet
run.

The SQLite table is append-only at the application layer. Each row stores
indexed columns for `ts`, `event`, `run_id`, `seq`, `transport`, `method`,
`tool`, `outcome`, `error_code`, and `duration_ms`, plus a JSON event body for
sanitized details. The database uses WAL mode, one connection, and a short busy
timeout to keep machine impact low for a single local long-running process.

Telemetry events currently include:

- `gateway.start`, `gateway.backend_ready`, `gateway.stop`, and runtime/startup failures;
- `mcp.request` for non-tool MCP requests observed by the SDK middleware;
- `tool.call` for every inbound MCP `tools/call`, including success, structured tool errors, SDK schema-validation errors, and protocol-level unknown-tool failures;
- `http.request` for `/mcp`, `/healthz`, and `/readyz` in HTTP mode.

Do not log raw note paths, host paths, note contents, tunnel credentials,
tokens, or exported personal data. SDK-observed known protocol methods and
registered tool names may be stored as canonical strings. Unknown
caller-controlled tool names, HTTP methods, and argument keys are classified
with bounded shape metadata and run-scoped hashes rather than stored raw.
SDK-unsupported MCP protocol methods are rejected by the SDK before gateway
telemetry middleware; proving telemetry for those methods would require a
future lower-level protocol wrapper. Path-like arguments are summarized by
presence, type, byte length, segment count, extension,
hidden/traversal/absolute flags, and a run-scoped hash so repeated attempts can
be correlated within a run without making the telemetry database a plaintext
vault index.

Required telemetry sinks are part of readiness. If SQLite or stderr telemetry
is configured and a post-start sink write fails, the gateway records degraded
telemetry state. In HTTP mode `/readyz` returns `503` with
`telemetry_degraded`; in stdio mode the process emits a sanitized stderr
diagnostic without writing to MCP stdout. `--telemetry off` is explicitly quiet.

Current first-slice resource budgets:

- `/mcp` HTTP request body: 1 MiB before SDK handling;
- stdio MCP message: 1 MiB through the repo-owned stdio transport;
- raw telemetry argument summary: 64 KiB;
- telemetry event body: 16 KiB after summarization;
- vault-relative path/base input: 4096 bytes and 128 segments;
- Obsidian tool operation timeout: 2 seconds.
- encoded SDK `CallToolResult` for every activated Obsidian tool: 64 KiB absolute, including text and structured content.

## Current Implementation

The first runnable gateway slice exists under `cmd/gateway/` and `internal/`.
It starts the same backend in either mode:

```bash
go run ./cmd/gateway stdio --obsidian-root /absolute/path/to/vault
go run ./cmd/gateway http --obsidian-root /absolute/path/to/vault --addr 127.0.0.1:8765
go run ./cmd/gateway stdio --obsidian-root /absolute/path/to/vault --telemetry-db /absolute/path/to/telemetry.sqlite
```

HTTP mode mounts:

- `/mcp` for SDK Streamable HTTP MCP requests;
- `/healthz` for process liveness;
- `/readyz` for local readiness when config, root accessibility, tool registration, and required telemetry are valid.

The HTTP listener accepts only explicit loopback hosts. Wildcard,
unspecified, public, and non-loopback hostname binds are configuration errors.
Readiness is local gateway readiness only; it does not prove OpenAI tunnel,
ChatGPT connector, `launchd`, or always-on suitability.

## Current Gaps

- `GAP-GW-002`: OpenAI Secure MCP Tunnel stdio startup, control-plane
  polling, ChatGPT app installation, and connector `initialize`/`tools/list`
  are proven, including exact discovery of the accepted five-tool surface. A
  bounded model-driven ChatGPT `ls` call, a fresh ChatGPT core-retrieval
  journey, and historical two-tool Codex `resolve` calls are proven; current
  five-tool Codex/ChatGPT-web `resolve` calls are not independently proven.
- `GAP-GW-003`: Current `launchd` readiness, bounded idle impact, and automatic
  crash recovery are proven. A multi-day soak and sleep/wake recovery cycle
  have not been measured.
- `GAP-GW-006`: Sanitized model-driven ChatGPT `ls`, `grep`, and continued
  `read_many` telemetry plus historical two-tool Codex `resolve` telemetry have
  been harvested. Current five-tool Codex/ChatGPT-web `resolve` telemetry has
  not. Local SDK subprocess and HTTP tests continue to prove the broader
  server-side matrix only.
