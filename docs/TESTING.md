---
title: "Testing"
status: draft
purpose: "Define proof expectations for the local MCP gateway."
covers:
  - cmd/
  - internal/
  - docs/requirements/
---

# Testing

Proof must match the claim. This repo handles personal data, so green unit tests are not enough for claims about vault safety, tunnel behavior, or low machine impact.

## Proof Surfaces

| Risk or behavior | Primary proof surface | Required when |
| --- | --- | --- |
| MCP tool registration and schemas | Boundary tests around `internal/mcp` and server/tool registration | Adding or changing tools |
| Vault path confinement | Filesystem adapter tests with temp fixture vaults, traversal cases, symlink escapes, and denied file patterns | Any filesystem behavior changes |
| Read-only guarantee | Integration test with before/after fixture-vault snapshot | Any Obsidian tool change |
| Search and listing limits | Large fixture tests with timeout, depth, byte, result, and cancellation assertions | Any traversal or search change |
| Structured telemetry | SQLite and JSONL proof matrix covering event families, sanitized identifiers, sink degradation, and no raw path leaks | Any audit or tool-call behavior change |
| Obsidian server tool names in ChatGPT | Live smoke test through OpenAI Secure MCP Tunnel | Before treating connector compatibility as settled |
| Minimal machine impact | Local process observation for idle CPU, memory, file descriptors, startup behavior, and no whole-vault startup scan | Before always-on usage |

## Expected Commands

Canonical test command:

```bash
go test ./...
```

When running inside a restricted agent sandbox that cannot write the default Go
build cache, keep the build cache repo-local:

```bash
GOCACHE=$(pwd)/.gocache go test ./...
```

The current suite includes:

- config validation and loopback bind rejection tests;
- root-confined filesystem adapter tests for traversal, absolute paths, hidden entries, symlink traversal, limits, cancellation, and read-only behavior;
- SDK subprocess stdio tests through `cmd/gateway`;
- process-level tests proving both production stdio/tunnel wrappers fail fast
  and do not print configured host paths when the vault root is unavailable;
- SDK Streamable HTTP tests through `/mcp`;
- `/healthz` and `/readyz` tests, including fail-closed readiness when the root disappears.
- structured telemetry tests for SQLite persistence, JSONL output, tool-call success and errors, HTTP request events, MCP request events, gateway lifecycle events, sink degradation, and subprocess stdio events written to a temp SQLite database.

## Structured Telemetry Proof Matrix

Local server-side telemetry proof must cover both JSONL and SQLite where
applicable. A valid proof pass includes:

- `tool.call` success, `path_denied`, `schema_validation`, `unknown_tool`, and
  `limit_exceeded` rows;
- decoded SQLite `body_json` assertions, not only row counts;
- indexed SQLite `method`, `tool`, `outcome`, and `error_code` assertions;
- hostile caller-controlled tool names and argument keys proving unknown values
  are bucketed or run-hashed rather than stored raw;
- `mcp.request` rows for non-tool MCP requests such as `tools/list`;
- `http.request` rows for `/healthz`, `/readyz`, and `/mcp`, including an
  oversized `/mcp` body rejection for known-length and unknown-length request
  bodies;
- HTTP SDK calls with oversized tool arguments that remain under the HTTP body
  cap and produce bounded telemetry summaries;
- `gateway.start`, `gateway.backend_ready`, `gateway.stop`, and at least one
  subprocess `tool.call` row from the CLI path;
- `gateway.backend_ready` tool names compared with SDK `ListTools`;
- the repo-owned stdio message limit using a raw oversized subprocess frame;
- post-start telemetry sink degradation using fake sink and real temp-SQLite
  failure paths, plus CLI close-failure stderr/exit proof;
- no raw vault root, host path, note path, note content, tunnel credential, or
  token material in JSONL text, SQLite indexed columns, or SQLite `body_json`.

Local SDK and HTTP tests prove only the server side. They do not prove
model-driven Codex behavior or ChatGPT connector behavior through OpenAI Secure
MCP Tunnel.
They also do not prove telemetry for SDK-unsupported MCP protocol methods,
which the SDK rejects before gateway middleware observes a request.

If linting or race tests are added, document the exact commands here before treating them as required gates.

## Test Data Rules

- Use generated fixture vaults in tests.
- Do not commit real vault files, exported personal data, secrets, or tunnel credentials.
- Keep fixture note content synthetic and small unless a specific performance fixture is needed.

## Live-Service Proof

Use live OpenAI tunnel/ChatGPT verification only for behavior that cannot be proven locally, such as connector compatibility for the `obsidian` server and its tool names. Record the date, server name, tool names tested, and observed result in closeout notes or the relevant requirement doc.

## Codex Temp-Profile Proof

For local Codex smoke tests, use a temp `CODEX_HOME` and `codex mcp add` so no
global MCP config is modified. A valid temp-profile setup must prove:

- `codex mcp list --json` shows the gateway as an enabled stdio MCP server;
- `codex mcp get <name>` shows the expected repo-local command and synthetic fixture vault;
- a non-interactive Codex run can discover and call `resolve` and `ls` from the configured `obsidian` MCP server;
- the configured temp SQLite telemetry database contains corresponding `tool.call` rows.

If the non-interactive Codex run requires an external model call and that call is
not allowed in the current execution context, record the boundary explicitly and
fall back to the local SDK subprocess proof. Do not copy Codex auth material into
a temp profile.

## Machine-Impact Proof

Always-on local use requires proof that the gateway is quiet when idle. Before enabling a launch agent or similar process manager, measure and record:

- idle CPU;
- memory;
- open file descriptors;
- startup duration;
- whether startup scans the whole vault;
- behavior when the vault is large or temporarily unavailable.
