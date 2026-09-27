---
title: "YNAB Domain"
status: draft
purpose: "Define the YNAB MCP integration and route its tool requirements."
covers:
  - docs/requirements/ynab-tools.md
---

# YNAB Domain

The `ynab` MCP server exposes YNAB's documented API through six typed tools:
`list`, `get`, `create`, `update`, `delete`, and `import_transactions`.
Its contract is [YNAB tool requirements](requirements/ynab-tools.md).
The [implementation plan](ynab-implementation-plan.md) records the accepted
delivery decisions and proof boundaries. The server is implemented locally;
live activation and authenticated provider checks are separate release work.

YNAB owns accounts, transactions, categories, targets, and budget assignments.
The adapter provides the same capability surface locally and through ChatGPT.
The local finance domain owns modeling, rules, monitoring, scheduling, and any
local storage or merging of fetched records. The MCP owns no synchronization
database, financial memory, proposal lifecycle, or workflow engine.
Explicit `create(type: export)` writes JSON or transaction CSV under a configured
private export root and returns artifact metadata. Full exports stay out of
model context; native ChatGPT downloads remain separately unverified.

## Endpoint and Transport

Publish this integration as the separate MCP server `ynab`, with simple tool
names. Keep Obsidian tools on `obsidian`. Use the existing official Go MCP SDK
and transport-independent composition: stdio for local clients and the existing
private OpenAI Secure MCP Tunnel approach for ChatGPT. The gateway's loopback
Streamable HTTP `/mcp` transport remains available. Start the built gateway with
`stdio --server ynab` or `http --server ynab`; HTTP defaults to `127.0.0.1:8768`.
The default server remains Obsidian. YNAB startup does not open the vault.

The upstream API is `https://api.ynab.com/v1`. Bind credentials through private
local configuration, never tool arguments or outputs. Writes are part of the
accepted tool scope, but documenting the endpoint does not authorize writes to
Eric's live financial records during development or testing.

## Ownership

The YNAB domain owns provider schemas, API requests, amount/date semantics,
batch dispatch/results, validation, and safe telemetry summaries. Reuse generic
gateway transport, configuration, limits, and audit facilities. Provider calls
must not pass through the Obsidian filesystem adapter or a generic shell tool.

The existing local YNAB CLI is implementation evidence, not the public contract.
Reuse suitable provider code only after checking these requirements. Its
reconciliation, classification, plan-hash, and approval workflows are not
requirements for the MCP. Local CLI access and MCP access must preserve the same
resource semantics; a local workflow can compose them externally.

Before implementing schemas, read the gateway's
[model-visible schema constraints](gateway.md#model-visible-tool-schemas).
The small Codex union probe is accepted evidence for proceeding. ChatGPT model
testing is explicitly deferred by Eric on 2026-09-06; it does not block these
requirements and must not be reported as completed compatibility proof.

## Local Configuration and Files

Use `.env.ynab.example` as the template for private `.env.ynab.local` and launch
local clients through `scripts/run-ynab-mcp-stdio.sh`. `YNAB_TOKEN` is required;
it reaches the gateway through the environment, never command arguments.
`YNAB_EXPORT_ROOT` optionally selects an absolute private directory. Otherwise
exports use the user configuration directory's `personal-mcp-gateway/ynab/exports`
child. The Mac default is under `Library/Application Support`.

Export requests provide an unused basename with the chosen format's extension,
not an arbitrary destination path. The gateway rejects symlinks and traversal,
creates private files, and publishes atomically without overwriting. Results
contain path, MIME type, byte count, and record count when known, not file bytes.
JSON preserves the provider response. CSV preserves integer milliunits and IDs,
includes split details as JSON, and prefixes formula-like text with an apostrophe;
null and empty cells are indistinguishable in CSV. Prefer JSON for lossless reuse.

Transaction lists require inclusive `since_date` and `until_date`. YNAB filters
from the start date; the adapter applies the end date locally. A narrow interval
does not guarantee a narrow upstream response. Provider bodies are capped at
32 MiB; ordinary MCP results at 192 KiB. Oversized reads return an explicit error,
not partial records or a usable change token. Full-plan exports deliberately fetch
once and write JSON; rendered exports are capped at 64 MiB.

## Supervision

YNAB has separate tunnel credentials, logs, health state, and LaunchAgent label
`com.ericfeunekes.personal-mcp-gateway.ynab-tunnel`. Release the shared gateway
binary before first installation, then use `make install-launchagent SERVER=ynab`.
`make verify-live SERVER=ynab`, `make restart SERVER=ynab`, and
`make uninstall-launchagent SERVER=ynab` select that service. Default commands
still select Obsidian. Configuration parsing treats environment files as data.

The existing release controller captures loaded services under one lock, including
crashed loaded YNAB jobs, replaces the shared binary once, and restarts Obsidian
then YNAB. Rollback uses that same captured set. It does not install absent services.
First-install failure unloads the new YNAB job and retains configuration for diagnosis.
Follow [local release](runbooks/local-release.md) for activation and acceptance.

Provider failures belong to tool calls, not local readiness. Calls have at most
10 items, a 60-second deadline, and 30-second provider requests; two upstream
requests can run concurrently and exports serialize. Writes are not retried.
An ambiguous write reports `uncertain` and stops later items; inspect provider
state before deciding whether to retry. Workflow scheduling remains external.

## Rate Limits and Recovery

All gateway processes for the same OS user coordinate through a private
`personal-mcp-gateway/ynab/provider-state/state.sqlite` beneath the user configuration
directory. This path is independent of export and telemetry configuration.
The store opens on the first provider call, not during metadata discovery or
readiness. It contains token hashes, request timestamps, and recovery state—no
tokens, financial records, resource IDs, or request bodies. Directory/file modes
are 0700/0600. Unavailable or invalid state fails closed before provider dispatch;
do not delete the store to bypass a cooldown.

The adapter reserves each actual HTTP attempt against 200 requests per rolling
hour per token. Native transaction batches consume one reservation; other item
batches can consume several, and safe-read retries consume another. Reservations
commit before dispatch and are not refunded, including after crashes. This is
deliberately conservative: it cannot prove whether a lost request reached YNAB.
Independent clients that bypass this gateway do not participate in local
accounting. YNAB remains the quota authority.
After cancellation, recording a received cooldown and releasing probe ownership
has a separate two-second bound; cancellation cannot silently discard known
provider backpressure. It never replays a request.

A 429 sets a shared token cooldown. A valid `Retry-After` supplies the earliest
retry time, with a one-second minimum; absent, malformed, or unrepresentable hints
use a conservative one-hour cooldown. Calls during cooldown return immediately
without contacting YNAB. The returned `retry_at` is the earliest gateway admission
time, not a promise that an external client has left provider quota available.

GET requests allow at most two attempts for transport interruptions, truncated
responses, or HTTP 408/500/502/503/504. Retry delay uses full jitter up to one
second and remains inside the call deadline. No 429, credential failure, invalid
request, malformed JSON/schema, or oversized response is retried. POST, PUT,
PATCH, and DELETE are never automatically retried, including bank import.
The upstream client uses a fresh HTTP/1 connection for each explicit attempt.
This prevents Go's connection-reuse or HTTP/2 retry machinery from bypassing
quota accounting. At this low request rate, the extra connection setup is an
accepted tradeoff for an enforceable attempt limit; tunnel transport is unchanged.

For each token and method/resource family, three transient failures open an
outage circuit for 30 seconds. Afterward only one request may probe recovery;
other callers fail promptly. A 45-second probe lease permits recovery after a
crash. Stale completions cannot clear newer failures. Server-directed transient
delays are also shared within that family. No background worker polls the API.
Cancellation is neutral, not successful recovery: canceling a probe returns it
to cooldown. A completed recovery fences failures from the older generation.

The published output schema adds optional `error_code`, `http_status`, `recovery`,
and `retry_at` fields to the v1 result contract (backward-compatible additions;
existing `status`, `error`, and `retry_after` remain). Error codes and recovery
actions are closed enums in `tools/list`; resource IDs remain runtime values.
Every batch containing a failed or unattempted item sets MCP `isError`, preserving
all indexed item results. Clients must inspect those results and must not replay
successful items or an entire partially applied batch.

| Recovery | Caller action |
| --- | --- |
| `fix_request` | Correct arguments, scope, or destination before another call |
| `fix_credentials` | Repair the private token or its permissions; the batch stops |
| `wait` | Wait until `retry_at`; do not poll repeatedly |
| `retry_read` | A later read is safe; its bounded automatic attempts were exhausted |
| `inspect_before_retry` | A write may have applied; inspect YNAB before deciding |
| `contact_operator` | Resolve local-state or provider-contract failure first |

Use `make test-ynab` for quota-free local proof. The [testing contract](TESTING.md#ynab-endpoint)
describes the simulator, subprocess tests, and the boundary between local proof
and live provider validation.
