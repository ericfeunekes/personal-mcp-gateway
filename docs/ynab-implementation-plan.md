---
title: "YNAB Implementation Plan"
status: implemented
purpose: "Record accepted YNAB delivery decisions and release proof boundaries."
---

# YNAB Implementation Plan

Implementation status, 2026-09-07: the six-tool endpoint, host-file exports,
SDK 1.7.0 compatibility changes, and shared-binary/two-service release wiring
are implemented in the working tree. This document retains the accepted plan;
current commands and behavior belong to [the domain](ynab.md). Local fixture
proof is recorded in [Testing](TESTING.md). Live activation, authenticated YNAB
reads, and explicitly authorized disposable write checks remain release work.

Implement the [accepted requirements](requirements/ynab-tools.md) using the
existing gateway and private tunnel approach. Ordinary retrieval now requires
explicit transaction date bounds, and exports write files on the gateway host
with metadata-only results. Implementation choices below incorporate the
completed experiments; production acceptance remains governed by the test plan.
Completed evidence is recorded in
[design validation](spikes/ynab-design-validation.md).

## Runtime and Dependencies

Use one gateway executable with separate integration processes on the same host.
Add an explicit integration selection with Obsidian as the existing default;
YNAB startup constructs its provider client and descriptors without opening a
vault. Make MCP name, icons, readiness, and telemetry integration-owned rather
than hardcoded to Obsidian. Share transport and audit code, not domain state.

Give YNAB its own LaunchAgent, stdio wrapper, private tunnel identity, configuration,
health marker, and log destinations. Local Codex invokes the same YNAB stdio
entrypoint. Preserve the existing Obsidian deployment. Extend the release
controller deliberately for the two supervised processes and shared executable;
test rollback/restart effects on both rather than adding an independent release
state store. Capture the installed/enabled service set under the existing release
lock, including loaded jobs whose process has crashed. Replace the binary once
and restart/check every captured service in fixed Obsidian-then-YNAB order.
Exclude YNAB when it has not been installed. Resume or roll back through the
existing release controller; rollback restores one binary and restarts the same
captured set. Service registration changes are clear-state operations. Extend
the existing manifest with per-service descriptors and keep one transaction slot.

The tunnel-client baseline is 0.0.14. On 2026-09-07 the official darwin-amd64
archive matched SHA-256
`75e10be774184fb42189e347b16eb6bc9fb0780135d8af714d34e30ce068dc53`.
It replaced 0.0.13 in the running checkout and was installed in this worktree.
The existing lock-aware restart and live verification passed. This proves local
supervision and tunnel readiness, not ChatGPT behavior.

Version 0.0.14 includes stdio recovery after request-ID reuse, redirect handling,
secret-redaction, and long-poll reliability fixes. Its Harpoon/multi-replica
features are unnecessary for this integration. See the
[release](https://github.com/openai/tunnel-client/releases/tag/v0.0.14).

The completed SDK assessment selected 1.7.0 with compatibility changes before
YNAB delivery. That dependency upgrade is implemented in this working tree.
Preserve SDK-added `resultType` and result `_meta` when substituting PDF content,
removing only the private document marker. Count the SDK-produced response
envelope in emitted-byte limits: the current 1 KiB reserve does not cover the
new server metadata with its embedded icon. Retain bounded document streaming;
the new SDK still marshals blob byte slices and does not replace that facility.

Use SDK `MaxRequestBodyBytes` at the existing 1 MiB boundary and consolidate
duplicate HTTP body reads, retaining bounded pre-dispatch native-batch rejection.
Enable `PropagateRequestCancellation` for new-protocol HTTP requests and prove
abandoned requests stop work; retain legacy cancellation and write deadlines.
Keep explicit union schemas and typed descriptors. The new protocol's sessionless
requests fit the existing stateless HTTP approach; subscriptions, sampling, and
new session-management machinery do not serve the accepted YNAB scope.

These recommendations come from the completed read-only task
"Assess MCP SDK upgrade and gateway simplification" on 2026-09-07. They require
legacy `2025-11-25` and new `2026-07-28` stdio/HTTP tests, actual emitted response
size checks, PDF identity/metadata/cleanup checks, and explicit false annotation
checks. The isolated SDK experiment reproduced metadata loss and a 71,056-byte
response exceeding the 64 KiB contract; after metadata preservation and an
8 KiB reserve, the MCP/app/entrypoint packages passed. Use SDK body-limit and
cancellation options proven by that experiment. Full production envelope and
activated-document tests remain required before release.
Explicit legacy/new stdio and HTTP journeys also passed in the isolated copy.
Current [tunnel guidance](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels)
continues to support stdio and private outbound connectivity. Associate YNAB with
Eric's actual personal Platform/ChatGPT contexts; do not introduce public ingress.

## Delivery Sequence

First apply the scoped SDK upgrade above with Obsidian regression proof. Its
protocol framing and output budgeting determine the YNAB response budget.

1. Finalize the six full input/output schemas against all 44 API operations.
   Use nonempty typed item arrays, including mixed resource types; retain all
   provider fields and limits required by the accepted contract.
2. Use the validated compact schema approach: typed item alternatives on input,
   per-result typed resource collections on output, concise field descriptions,
   and flattened export relationships retaining parent IDs/month context. The
   full six-tool fixture passed Codex visibility and input construction; repeat
   size/validation checks after final result and continuation metadata is added.
   Raw nested provider response unions were experimentally ruled out.
3. Require `since_date` and `until_date` in every transaction-list schema,
   including scoped and incremental requests. Reject missing/reversed bounds;
   month-scoped bounds stay within the requested month. Return the bounded
   interval directly or an explicit size error directing the caller to narrow
   it. Do not build repeated-refetch pagination or a snapshot store.
   Keep complete history deliberate: `create(type: export, source: plan)` fetches
   once and writes a full JSON file; transaction exports require both dates and
   support JSON or CSV. `get(type: plan)` returns plan metadata. The experimental
   8 MiB body and 48 KiB record caps are not production defaults. Use the
   selected ceilings below, with no artificial pagination of ordinary reads.
   Configure one private export root; accept confined relative filenames with
   exclusive creation. Return only artifact metadata. Local Codex uses the saved
   path; ChatGPT can request a file saved on the Mac, but native download/attachment
   delivery remains deferred. No response snapshot paging store is needed.
4. Implement typed YNAB provider access and domain descriptors. Assess existing
   CLI provider models as evidence; keep its classification/proposal workflows
   external. Set credential wiring through private local configuration; tools
   receive no secrets or arbitrary provider URLs.
5. Add integration selection, independent readiness, and the YNAB local/tunnel
   wrappers. Apply the supervision and shared-binary policy below and prove it
   through the actual release controller before changing the active runtime.
6. Complete provider boundary tests, MCP transport tests, full-schema Codex
   calls, and repository checks. Then land and deploy through the existing
   release process. Register YNAB's private tunnel separately from Obsidian.

## Selected Implementation Defaults

These are implementation choices, not claims that the sizing fixture proved
capacity at every ceiling. Acceptance tests must exercise them before release.

- Select `--server obsidian|ynab` after the existing `stdio` or `http` mode;
  default to Obsidian for current callers. YNAB HTTP defaults to loopback port
  8766, leaving Obsidian's 8765 unchanged. Normal local/tunnel use remains stdio.
- Use a narrow Go `net/http` provider client and typed domain models. Do not
  launch the Python CLI per tool call or import its workflow database. Reuse
  documented provider behavior and fixture cases from that CLI where useful.
- Use separate `.env.ynab.local` configuration parsed as bounded data, matching
  the existing environment-file rules. The YNAB wrapper consumes `YNAB_TOKEN`,
  `YNAB_EXPORT_ROOT`, the tunnel identity/key, and existing executable/state
  settings. Resolve secrets through the established private setup/1Password
  workflow, pass them by environment, and strip them before tests/builds.
- Default the export root to the per-user gateway state directory's
  `ynab/exports` child; permit an explicitly configured absolute root. Tool
  inputs accept only a new basename under that root, with the requested format's
  extension. Create directories/files privately; no overwrite or arbitrary
  directory traversal. Completion publishes the file atomically from a sibling
  staging file, then returns metadata. No background export worker or registry.
- Support full-plan JSON and date-bounded transaction JSON/CSV. Preserve the raw
  complete JSON response; CSV has ID/account/date/amount/category/payee/memo/
  cleared/approved/flag/deleted fields and a JSON split-details column. Document
  literal text escaping and distinguish null/empty limitations from JSON fidelity.
- Limit a call to 10 items, a provider response to 32 MiB, a completed export to
  64 MiB, and an ordinary emitted MCP result to 192 KiB. These are fixed limits,
  not caller tuning knobs. Use a 60-second call deadline and 30-second provider
  request deadline, capped by remaining call time. Admit at most two provider
  requests concurrently per process and serialize export materialization so
  multiple clients cannot multiply decode/file-building memory without bound.
- Preserve input order and the accepted stop-on-error mutation policy. Use
  native batches only for consecutive compatible transaction items. Return
  bounded identity/status for writes; callers get details with `get`. For reads,
  return explicit oversize errors with no partial-success or usable change token.
  Set ordinary text content to a short summary instead of duplicating full
  structured JSON. Include SDK-added envelope bytes in size tests.
- The accepted recovery follow-up adds at most one retry for safe GET requests,
  shared per-token quota/cooldown, and scoped outage suppression. Writes are
  never replayed automatically and there are no background retry loops. Current
  defaults and caller actions belong to [rate limits and recovery](ynab.md#rate-limits-and-recovery).
- Install YNAB using `make install-launchagent SERVER=ynab`, selecting the named
  wrapper, configuration, logs, health marker, and fixed YNAB LaunchAgent label.
  Default administrative commands continue to address Obsidian. `make release`
  and rollback always address the captured installed/enabled service set.
  Installation changes are blocked during an active release transaction.
- Release the YNAB-capable shared binary before first YNAB service installation.
  Extend the manifest version with a service-descriptor list only for new
  clear-state transactions; existing pending releases finish through their
  pinned controller. A failed first YNAB installation unloads that newly added
  service and retains its config for diagnosis; it does not replace Obsidian's
  accepted binary. Do not silently install missing services during release.
- Local readiness checks configuration/client construction; it does not poll
  YNAB or treat upstream availability as local health. Runtime provider failures
  surface on the affected tool call. Keep health/telemetry integration-specific.

Export completion means a file exists on the Mac. Phone delivery is an external
presentation/transfer action when requested. Native ChatGPT JSON/CSV download
or sandbox-file materialization is deferred and is not part of this release's
completion claim.

Eric explicitly accepted this export delivery boundary on 2026-09-07: either
client calls the MCP export operation, the Mac writes beneath its configured
private root, and the caller receives metadata rather than file contents.

## Required Proof

- Provider HTTP fixtures: exact routes/fields, nullable updates, units, IDs,
  splits/transfers, targets, recurrence, imported-record duplicate handling,
  linked-bank import, batch stop policy, rate limiting, and uncertain writes.
- Retrieval: required dates on every transaction-list branch, missing/reversed
  bounds, month-boundary validation, large exports, older history, deleted
  records, explicit oversize errors, cancellation, and change-token completeness.
- Exports: complete JSON round-trip, CSV quoting/units/IDs/splits/formula-like
  text, metadata-only MCP results, file confinement, name conflicts, cancellation
  and incomplete-file cleanup. Native ChatGPT file downloads are not claimed.
- Runtime: Obsidian remains usable; YNAB requires no vault; credentials stay
  out of tool metadata/logs; independent failure/readiness; shared-binary
  replacement, restart, and rollback are correct for both services.
- Run `make test` and the applicable local release gates. Perform authenticated
  read-only YNAB smoke calls once credentials are configured. Live write tests
  need explicitly authorized disposable targets.
- ChatGPT model testing remains deferred by Eric. Do not turn that deferral into
  a claim of proven ChatGPT invocation or omit the local Codex/schema checks.
