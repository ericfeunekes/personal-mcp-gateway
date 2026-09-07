---
title: "YNAB Design Validation"
status: draft
purpose: "Record isolated experiments that settle YNAB implementation choices."
---

# YNAB Design Validation

Experiments run on 2026-09-07 against the accepted YNAB API 1.86.0 coverage.
These are planning fixtures, not an implemented YNAB server. Production source,
dependencies, financial data, and supervised services were not changed.

## Full Tool Schemas

The schema fixture traversed all 44 provider operations, expanded provider
types, and built the six typed-array tool inputs. The actual Codex renderer
from commit `52e12e0cb` was compiled in isolation without changing its limits.

Naively embedding provider response wrappers made list/get/create/update output
schemas render as `unknown`, even without descriptions. Verbose update input
also exceeded rendering constraints. Flattening object inheritance and keeping
concise descriptions made all input schemas visible. Per-result typed resource
collections eliminated repeated nested output definitions.

| Tool | Rendered input bytes | Rendered output bytes | Lost alternatives |
| --- | ---: | ---: | --- |
| list | 2,314 | 6,629 | None |
| get | 693 | 6,629 | None |
| create | 2,229 | 6,629 | None |
| update | 3,056 | 6,629 | None |
| delete | 172 | 6,629 | None |
| import_transactions | 60 | 6,629 | None |

A fresh Codex CLI 0.147.0 / gpt-5.6-sol session used only exposed metadata,
with file and shell inspection prohibited. Seven successful fixture calls
covered every verb, one-item and mixed arrays, historical reads, a split,
category targets, scheduled transactions, enums, and explicit null/omission.
The model identified all input alternatives and all 15 output collection types.
The actual repository JSON Schema validator separately accepted all seven calls
and rejected empty arrays, unsupported account deletion, invalid cleared enum,
and fractional milliunits.

The echo responses instantiated index/status only. This establishes visibility
and input construction, not provider correctness or real output projection.
Production projection must preserve parent IDs, month context, and all provider
fields when flattening full exports. Its final metadata and continuation fields
still require a size check. Retain six tools; do not copy large raw response
wrapper unions into their output schemas.

## Shared Executable and Two Services

A scratch fixture used real child processes and temporary executable paths to
exercise four scenarios: both services updated then rolled back; YNAB absent;
YNAB readiness failure; and interruption between service restarts. All four
passed in 18.19 seconds. No production LaunchAgent was touched.

The experiment supports a single binary replacement and a service set captured
by the existing release authority. Restoring the binary requires restarting
every service in that set, including a service that already started the candidate.
YNAB not installed is excluded. A crashed but installed/enabled service must
not be mistaken for an absent integration merely because no process is running.

This fixture does not exercise the production release controller or launchd.
Its injected interruption is not proof of controller crash recovery. Preserve
the existing explicit resume/rollback contract; automatic rollback in a fixture
is not authorization to change production recovery policy. Production tests must
exercise manifest migration, both service descriptors, restart/readiness,
first installation, and rollback through the actual controller.

## Retrieval Decision

The localhost experiment demonstrates that output paging does not reduce the
upstream body when the provider supplies no pagination. Date/account scoping
can reduce that body. A stateless cursor must bind to the request and unchanged
observed source; provider delta knowledge is not itself a page cursor.

Repeated full reads plus a content fingerprint can detect changes even on routes
without provider version tokens. They cannot guarantee export completion under
continuous edits. Short-lived snapshots avoid repeated reads but introduce
server-held financial data and lifecycle limits. Eric subsequently selected
mandatory transaction date bounds and deliberate full-history retrieval on
2026-09-07. Ordinary reads therefore do not use repeated-refetch pagination;
short-lived snapshots were not authorized. Full-export delivery limits remain
a separate implementation decision.

The initial fixture's 8 MiB upstream, 192 KiB output, and 48 KiB record caps are
experimental values, not adopted production limits. Some initial memo payloads
deliberately exceeded the provider's 500-character limit as stress cases.
Final sizing must distinguish valid provider-shaped data from over-limit tests.

A corrected provider-valid fixture with 9,179 transactions and 500-character
memos produced 5,641,197 bytes, requiring 29 output pages at the experimental
192 KiB page size. Full stateless rereads transferred 163,594,713 bytes (about
156 MiB). The 9,000-record stress fixture with invalid 1,800-character memos
was 18,234,094 bytes and would require 94 pages / about 1.60 GiB of rereads.
Neither is a measurement of Eric's private data or real YNAB latency.

The corrected localhost fixture passed unchanged no-token continuation and
detected a memo change despite unchanged provider knowledge. Canonical local
sorting plus a fingerprint of all output-relevant fields establishes equality
of the observed contents; it needs no upstream ordering guarantee. Bind the
query, requested delta start, fingerprint, and returned provider token (when
present) to the cursor. This does not prove that YNAB internally supplies an
atomic snapshot, or eliminate restart starvation.

## SDK 1.7.0 Compatibility

An isolated copy upgraded to the published SDK 1.7.0. The unchanged native
document substitution dropped `resultType` and result `_meta`. The existing
`TestPhase1LongNamesFitExactSDKResultBudget` also failed: 71,056 emitted result
bytes exceeded the 65,536-byte contract. This reproduces the assessment's two
compatibility concerns rather than treating the dependency bump as safe.

Minimal scratch fixes preserved protocol metadata while removing the private
document marker, and increased the existing response reserve from 1 KiB to
8 KiB. After these fixes, `internal/mcp`, `internal/app`, and `cmd/gateway`
tests passed uncached. The 8 KiB reserve fixes the tested current metadata;
production must bind its budget to the emitted envelope and keep a regression
test when server identity/icons change.

SDK option probes rejected declared-length and unknown-length bodies over
1 MiB before handler dispatch, and propagated new-protocol HTTP cancellation
to the tool context. A representative 60 KiB output had 729 bytes of additional
wire overhead using the probe's small icon; that is not the larger production
Obsidian icon's overhead.

Full activated PDF capacity/process proof and production HTTP wrapper
consolidation remain implementation obligations. These probes establish SDK
feasibility, not release acceptance or live ChatGPT behavior.

Explicit raw protocol journeys passed for `2025-11-25` (initialize, initialized,
tool call) and `2026-07-28` (discover, tool call), over both stdio and Streamable
HTTP. Legacy responses omitted `resultType`; new responses emitted `complete`.
The final uncached MCP/app/entrypoint package run passed after adding these tests.

## Required Dates and File Exports

The subsequent required-date fixture rejected each missing bound across all
five transaction scopes. Separate interval checks covered reversed dates,
invalid calendar dates, and cross-month bounds. A fresh Codex 0.147.0 /
gpt-5.6-sol session identified both dates as required in every transaction
alternative and made one successful six-item call: five transaction scopes
with dates plus an account listing without dates. This is schema/fixture proof;
the production Go handlers still need the same validation tests.

Eric clarified that full exports should be written as files, with metadata
returned to the model. A synthetic file-output experiment passed three tests:
lossless JSON round-trip, CSV quoting/IDs/milliunits/split details and literal
formula-like text, and ZIP packaging with intact JSON plus CSV. With 9,179
synthetic records, the JSON file was 5,820,555 bytes and its metadata only 85
bytes. A highly repetitive synthetic ZIP was 123,570 bytes; this is not a
prediction of real financial data compression. Task-owned outputs were removed
by the fixture. V1 adopts JSON and transaction CSV; ZIP is only tested prior art.

These tests resolve full export's model-context problem through file creation,
not response paging or a retained response snapshot. They do not prove atomic
publication, confinement, interrupted-write cleanup, or client download handling;
those remain production boundary tests. Both clients can request server-side
file creation; that does not imply either client downloaded the file.

Codex source inspected at `c0b628571` serializes unknown MCP content such as
resource links as model-facing text, while its TUI summarizes resource links.
Its `openai/fileParams` machinery uploads tool input files, not arbitrary tool
output materialization. The existing Obsidian native delivery proof covers PDF,
not JSON/CSV downloads. Therefore the plan returns a host-local artifact location
without promising ChatGPT file download, a public URL, or model-readable bytes.
Sources: [model content conversion](https://github.com/openai/codex/blob/c0b628571/codex-rs/protocol/src/models.rs),
[file input handling](https://github.com/openai/codex/blob/c0b628571/codex-rs/core/src/mcp_openai_file.rs).

With required dates, the list input renders at 2,669 bytes. Adding representative
file-export alternatives raises create input to 2,649 bytes; artifact metadata
raises the common output to 6,811 bytes. All six remain free of `unknown` in the
renderer. The export fixture covers format serialization, not full production
source scopes or filesystem confinement. The plan's 32 MiB upstream and 64 MiB
file ceilings are selected headroom, not stress-tested maxima; test those exact
boundaries during implementation.

## Reproducibility

Task-owned harnesses and raw results remain under
`.scratch/ynab-validation-spike/` for the remaining production-schema work:
`schema_probe.py`, `compact_outputs.py`, `render.rs`,
`validate_calls.go`, `model-results.md`, and the `retrieval`, `deployment`, and
`sdk` lanes. They contain synthetic data only. They are not production tools or
a permanent parallel authority for financial records.
