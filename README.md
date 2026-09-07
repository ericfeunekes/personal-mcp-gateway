# Personal MCP Gateway

Local Go MCP gateway for exposing selected personal systems to ChatGPT through OpenAI's Secure MCP Tunnel.

Initial target integrations:

- YNAB
- Obsidian
- Voicenotes

The [YNAB endpoint](docs/ynab.md) has [accepted requirements](docs/requirements/ynab-tools.md)
for typed retrieval, management, bank import, and private JSON/CSV exports.
It is implemented with separate stdio/HTTP server selection and tunnel wiring;
live YNAB activation is not part of the local implementation proof.

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

The accepted Obsidian server implements the five-tool core-retrieval slice plus
the five-tool mutation delta:

- `resolve`
- shallow `ls`
- bounded single-note `read`
- aggregate-budgeted `read_many`
- deterministic content `grep`
- mutation-scoped `stat`
- complete-value `write`
- structured exact-context `edit`
- absent-destination `move`
- permanent `delete`
- stdio mode
- loopback HTTP mode with `/mcp`, `/healthz`, and `/readyz`
- SQLite-backed structured telemetry for MCP calls, HTTP requests, and gateway lifecycle events
- local request, argument, path, telemetry-event, and tool-operation budgets
- explicit MCP impact annotations: the original five tools and `stat` are
  read-only, while `write`, `edit`, `move`, and `delete` are destructive; all
  ten tools are closed-world and the mutation delta is precondition-idempotent
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
runtime still recoverable. Refresh connector metadata when model-visible
descriptors changed; make a representative affected call when their wording or
schema governs tool selection or arguments. Complete every other applicable
change-scoped journey, then accept that full release ID; use exact rollback when
evidence implicates the candidate.
`make release-status` is a bounded diagnostic and recovery aid, not an extra
step on the successful fast path. Local readiness is not substituted for model
proof when model-visible behavior changed.

Release/update and repo-owned restart/install/uninstall commands share one
fail-fast lifecycle lock. `make update` may fetch without it, but revalidates a
clear release slot, clean `main`, and the fetched ref while holding the lock
before fast-forwarding. An interrupted `prepared` release is resumed by rerunning
`make release` with the same immutable candidate rather than rebuilding it.

See the [local release runbook](docs/runbooks/local-release.md),
[testing contract](docs/TESTING.md), [tunnel runbook](docs/runbooks/openai-tunnel.md),
and [gateway behavior](docs/gateway.md) for setup, proof, and operational
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
proven through sanitized local telemetry. The accepted five-tool surface also
has a fresh ChatGPT model-selected `grep` -> `read_many` -> continued
`read_many` journey with cursor-bound request/budget reuse and no retained note
identity or content. Model-driven `resolve` was historically proven through
Codex on the prior two-tool surface, including after automatic LaunchAgent
recovery; current five-tool Codex and ChatGPT-web `resolve` calls remain
surface-parity proof gates.
Exact-candidate proof is deliberately split: synthetic fixtures cover retrieval
semantics, current-vault probes cover broad `grep`, inventory, performance, and
resources, and the authenticated journey covers live grouped retrieval.

## Operating Principles

- OpenAI Secure MCP Tunnel is the external transport boundary for ChatGPT access.
- Local services should not listen on a public interface.
- Read-only tools are the default until write paths are explicitly designed.
- Each integration should have its own MCP server name and narrow capabilities instead of generic file or API access.
- Secrets stay local and out of the repo.
- Logs should prove what was accessed without storing sensitive content by default.
- Reliability and low machine impact outrank breadth of features.

## Remaining Design And Proof Gaps

- Tool compatibility: ChatGPT accepts the `obsidian` server and displays
  exactly `resolve`, `ls`, `read`, `read_many`, and `grep` as read-only.
  Representative `ls` and core-retrieval execution is proven. Codex
  model-driven `resolve` is historical two-tool evidence; graph tools and
  current five-tool Codex/ChatGPT-web `resolve` invocations remain unproven.
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
