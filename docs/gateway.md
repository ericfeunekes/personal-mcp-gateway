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

The gateway uses the official Go MCP SDK, `github.com/modelcontextprotocol/go-sdk`
v1.7.0, including `mcp.StdioTransport` and stateless Streamable HTTP. HTTP uses
the SDK's 1 MiB request-body limit and request-cancellation propagation, with
bounded native-document batch rejection before dispatch. Native document output
preserves SDK `resultType` and result metadata; output budgets reserve 8 KiB for
the SDK envelope. Do not implement a separate MCP JSON-RPC stack.

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

## Model-Visible Tool Schemas

Apply these constraints when adding or changing any integration's MCP tools.
Publish complete input schemas: internal Go types alone are not visible to the
model. Use explicit resource discriminators and resource-specific object
alternatives for polymorphic inputs. An array of those alternatives can represent
both single-item and mixed-resource batches without separate single/batch tools.
Batch execution and partial-failure semantics still belong to the domain contract.

Codex Code Mode renders tool JSON Schema into TypeScript declarations. The
inspected renderer preserves `const` literals, enums, `anyOf`/`oneOf` unions,
arrays, required versus optional properties, nullable types, and property
descriptions as comments. This is a presentation of the schema, not its complete
validation semantics: `integer` becomes `number`, and constraints such as
`minItems` are not expressed in that TypeScript type. Explain consequential
constraints in field descriptions and enforce them in server validation. Supply
an output schema when callers need structured result types; the echo probe below
supplied none and consequently exposed only `CallToolResult`.

The inspected Codex renderer has these implementation limits:

- An individual rendered schema exceeding 16,000 bytes becomes `unknown`.
  This is a rendered-schema limit, not an MCP payload limit or a tool-count limit.
- Local reference expansion is bounded to two expansions per path and 32 total
  expansions. Exhausted or unsupported reference expansion can produce `unknown`.
- Intermediate rendering work has a separate 64,000-byte budget; exhausting it
  can also lose type detail before the final size check.

Inspect the complete model-visible declarations for new or materially expanded
schemas, including output types. Keep descriptions concise and check expanded
unions/references against the renderer limits. Verify that the model can see all
alternatives and construct representative calls; server `tools/list` success
alone does not prove that client rendering retained the schema. For batch tools,
exercise one-item and multi-item calls, mixed types where supported, enum values,
and omission versus explicit null. Verify server rejection of invalid inputs
separately from model-visible type presentation.

Evidence boundary, 2026-09-06: a harmless stdio echo tool was exercised with
Codex CLI 0.147.0 and gpt-5.6-sol. With file/shell inspection prohibited, the model
reported both transaction/category alternatives, required fields, enums,
optional fields, and null-description comments; one-item and mixed-item calls
succeeded. The fixture had truthful read-only annotations. This proves the small
input union in that client, not the full YNAB schema, output-schema behavior, or
ChatGPT compatibility. Test each target client independently.

The subsequent [full YNAB schema experiment](spikes/ynab-design-validation.md)
demonstrates why rendered-size checks matter: nested provider response unions
collapsed to `unknown`, while typed resource collections retained every exposed
alternative. Treat final production projection and result validation separately
from the fixture's model-visible type proof.

YNAB uses `NewExactJSONToolDescriptor` at the SDK registration seam. SDK typed
argument validation passes through floating-point decoding, which rounds integer
milliunits above 2^53. The adapter validates a number-preserving view, dispatches
the original JSON bytes, and validates/emits exact JSON output through official
SDK `AddTool`. It does not apply schema defaults. Use this descriptor when exact
JSON numbers are part of a tool contract; ordinary typed descriptors remain
appropriate for other tools. Raw HTTP ingress and egress tests cover this boundary.

Source limits were inspected at Codex commit `52e12e0cb`, separately from the
installed CLI version. Treat them as version-specific and recheck when upgrading:
[schema renderer](https://github.com/openai/codex/blob/52e12e0cb/codex-rs/code-mode-protocol/src/json_schema_types.rs)
and [tool declaration assembly](https://github.com/openai/codex/blob/52e12e0cb/codex-rs/code-mode-protocol/src/description.rs).

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
LaunchAgent, and then leaves the release pending change-scoped proof. Connector
metadata and model proof apply when the release changed that boundary; local
lifecycle proof applies to lifecycle-only changes.

The public release contract has four commands: `make release`, diagnostic
`make release-status`, and exact-ID `make release-accept` /
`make release-rollback`. The normal fast path omits status: release, refresh
connector metadata when it changed and complete every applicable proof row in
`docs/TESTING.md`, then accept or roll back that same full release ID. An interrupted `prepared`
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

`/mcp` accepts a request only when its `Host` names loopback (`127.0.0.1`,
`::1`, `localhost`) or the single exact DNS name given by `--allowed-host`.
Any other `Host` gets 403 before dispatch. This replaces the SDK's
loopback-only DNS-rebinding check, which would otherwise reject the tailnet
name that `tailscale serve` forwards unchanged. `/mcp` also rejects cross-site
browser requests (Go `http.CrossOriginProtection`). Non-browser MCP clients
send no `Origin` or `Sec-Fetch-Site` and are unaffected. `--allowed-host` is
valid only in HTTP mode and accepts no wildcard, port, or IP literal.

## Tailnet HTTP Services

Separately from the ChatGPT tunnel, two optional LaunchAgents serve Obsidian
and YNAB over HTTP to clients on Eric's tailnet (currently Muse):

| Service | LaunchAgent label suffix | Loopback address | Tailnet path |
| --- | --- | --- | --- |
| Obsidian | `obsidian-http` | `127.0.0.1:8765` | `/obsidian/mcp` |
| YNAB | `ynab-http` | `127.0.0.1:8768` | `/ynab/mcp` |

- The gateways still bind loopback only. `tailscale serve` (tailnet-only,
  never `funnel`) is the sole path in, and it forwards the Mac's `ts.net`
  name, which the service allows through `MCP_GATEWAY_ALLOWED_HOST` in the
  domain's existing env file.
- **Tailscale policy is the access boundary.** There is no bearer token. Any
  device the tailnet policy lets reach this Mac on tcp:443 gets the full
  Obsidian and YNAB tool surface, including writes. The policy must admit
  only Eric's own devices.
- Each HTTP service shares its domain's env file, state directory, telemetry
  database, and (for YNAB) quota database with the stdio tunnel process, so
  both YNAB processes draw on one quota budget.
- The release transaction captures, restarts, health-checks, and rolls back
  these services when they are loaded, like the optional YNAB tunnel.

Operator steps: [tailnet HTTP runbook](runbooks/tailnet-http.md).

## Current Gaps

- `GAP-GW-003`: Current `launchd` readiness, bounded idle impact, and automatic
  crash recovery are proven. A multi-day soak and sleep/wake recovery cycle
  have not been measured.
