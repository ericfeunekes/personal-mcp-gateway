# Personal MCP Gateway

Local Go MCP gateway for exposing selected personal systems to ChatGPT through OpenAI's Secure MCP Tunnel.

Initial target integrations:

- YNAB
- Obsidian
- Voicenotes

## Intent

Run personal MCP tools locally, keep the origin private, and publish access to ChatGPT only through OpenAI's outbound Secure MCP Tunnel. The gateway should make personal data useful to external AI tools without creating a broad public API or a generic filesystem proxy.

The target is an MCP server named `obsidian` with read-only agent tools over the local vault:

- `ls`
- `resolve`
- `read`
- `read_many`
- `grep`
- `links`
- `traverse`
- `backlinks`
- `path_between`

`grep` is the universal content-discovery entry point. Graph operations are
explicit, bounded expansions over authored references; they do not perform
semantic search or hidden vault-wide traversal.

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
- `launchd` supervision with a measured idle footprint and automatic recovery
  after a forced tunnel-process exit

Local commands:

```bash
go run ./cmd/gateway stdio --obsidian-root /absolute/path/to/vault
go run ./cmd/gateway http --obsidian-root /absolute/path/to/vault --addr 127.0.0.1:8765
make test
```

## Local Release

After a code change has landed, update and deploy the local always-on runtime
with:

```bash
make update
```

`make update` requires a clean `main`, fetches `origin`, fast-forwards to
`origin/main`, requires an exact commit match, and then runs the complete local
release. Use `make release`
when the desired clean commit is already checked out. The agent-facing release
surface is:

```bash
make release
make release-status
make release-accept RELEASE_ID=<full-id>
make release-rollback RELEASE_ID=<full-id>
```

`make release` runs the local test, exact-candidate MCP, installation, restart,
and readiness gates, then leaves the exact candidate `pending` with its previous
runtime still recoverable. Refresh connector metadata and complete the required
model-selected journey, then accept that full release ID; use exact rollback if
the external proof fails. `make release-status` is a bounded diagnostic and
recovery aid, not an extra step on the successful fast path. Local readiness is
never treated as model proof.

Release/update and repo-owned restart/install/uninstall commands share one
fail-fast lifecycle lock. `make update` may fetch without it, but revalidates a
clear release slot, clean `main`, and the fetched ref while holding the lock
before fast-forwarding. An interrupted `prepared` release is resumed by rerunning
`make release` with the same immutable candidate rather than rebuilding it.

See `docs/runbooks/local-release.md` for setup, target details, and proof
boundaries.

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
proven through sanitized local telemetry. Model-driven `resolve` is proven
through Codex using the installed `Obsidian` app, including after automatic
LaunchAgent recovery. A ChatGPT-web-specific `resolve` prompt remains a
surface-parity proof gate.

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
  execution is proven. Codex model-driven `resolve` is also proven through the
  installed app; a ChatGPT-web-specific `resolve` invocation is not.
- Auth mapping: how OpenAI connector identity maps to allowed MCP capabilities.
- Deployment: the current `launchd` runtime has passed bounded idle-impact and
  automatic crash-recovery proof; a multi-day soak and sleep/wake cycle are not
  part of the current proof.
- Telemetry operations: retention, compaction, and optional encryption are not designed yet.

## Docs

- `docs/ARCHITECTURE.md` records the gateway shape and domain boundaries.
- `docs/obsidian.md` owns the Obsidian MCP server contract.
- `docs/requirements/obsidian-filesystem-tools.md` defines the target agent tool surface and its performance gates.
- `docs/TESTING.md` defines proof expectations for reliability, robustness, and minimal machine impact.
- `docs/runbooks/local-release.md` defines the landed-code-to-local-runtime release and update flow.
