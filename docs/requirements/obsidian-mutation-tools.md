---
title: "Obsidian Mutation Tools"
status: blocked
purpose: "Define the trusted personal-vault mutation contract for the obsidian MCP server."
covers:
  - internal/tools/obsidian/
  - internal/fsx/
  - internal/mcp/
  - internal/audit/
---

# Obsidian Mutation Tools

## Outcome

An authorized operator using the personal `obsidian` MCP server can inspect mutation-ready identity, create, write, edit, move, and permanently delete allowed vault files. Operations are explicit, stateless, root-confined, bounded, and truthful about whether a change occurred. This trusted personal connector advertises mutation tools by default; it has neither a separate local write-mode switch nor a multi-user authorization system.

The vault remains the source of truth. The gateway owns neither a shadow copy nor durable mutation state. Metadata-only audit data is evidence of an operation, not an undo log or a second authority.

## Feasibility Gate

This contract is not currently executable on the supported macOS filesystem
boundary. On volumes that advertise the required capability, macOS provides
atomic absent-destination enforcement through `renameatx_np(..., RENAME_EXCL)`;
unsupported volumes return `ENOTSUP` and must fail closed. macOS does not provide a public namespace
operation that conditionally replaces, moves, or removes an existing name only
when that name still has the caller-supplied complete source-version stamp. A `stat` or
`fstat` check followed by `renameatx_np`, `renameat`, or `unlinkat` therefore
retains a final-syscall race with Obsidian or another external writer.

Do not implement or advertise the mutation tools by weakening fingerprint
preconditions to a last-moment check. `RENAME_SWAP` is not a substitute: it
changes both names before the displaced identity can be verified, so a rejected
call would no longer leave both paths unchanged. Advisory locks or a cooperating
Obsidian plugin also do not cover unrelated filesystem writers and therefore do
not satisfy this contract.

Implementation may resume only after a durable requirement revision names an
enforceable authority boundary. Acceptable directions are a supported primitive
that binds the complete expected source-version stamp—including same-object
content changes and directory-membership changes—to the namespace effect, or an
explicitly different product contract approved by Eric. Until then, issue #3
and its implementation children remain blocked and no mutation-scoped tool is
added to the then-current accepted server surface.

Primary platform evidence:

- Apple documents exclusive rename as an absent-destination volume capability:
  <https://developer.apple.com/documentation/foundation/urlresourcevalues/volumesupportsexclusiverenaming>.
- Apple's APFS guide lists `renameatx_np` as the safe-save primitive:
  <https://developer.apple.com/library/archive/documentation/FileManagement/Conceptual/APFS_Guide/ToolsandAPIs/ToolsandAPIs.html>.
- The macOS SDK `rename(2)` contract documents atomic swap, exclusive
  destination, no-follow, and beneath-resolution flags; no public flag accepts
  an expected source version. The SDK header's additional `RENAME_SECLUDE` flag
  also carries no expected-version argument.

## Public Capability

The `obsidian` server adds a read-only `stat` tool and first-class `write`, `edit`, `move`, and `delete` tools alongside discovery and read tools. They never accept an absolute host path, shell expression, arbitrary URL, or server-side current directory. Every target and destination is an explicit vault-relative path and passes the same hidden-path, traversal, symlink, and root-confinement rules as reads.

`stat` returns canonical safe metadata plus an opaque current-source fingerprint for one existing allowed regular file or directory. It is the public fingerprint-acquisition path for attachments and directories; `read` continues returning the same 43-character unpadded base64url fingerprint with Markdown content. `stat` does not return content, follow symlinks, or expose raw filesystem identity.

`write` creates a file or replaces one complete file value of at most 524,288 decoded bytes. `edit` applies one atomic structured patch to one existing regular file of at most 8,388,608 bytes and produces a file no larger than that same limit. A patch contains 1 through 64 ordered replacement operations. Each replacement carries a non-empty `old` value and a `new` value using one patch-wide explicit `utf8` or `base64` encoding; each decoded value is at most 524,288 bytes, and all decoded old/new values together are at most 524,288 bytes. Every `old` value must match exactly once in the same fingerprinted original source, replacement spans must not overlap, and the entire patch is validated against that original source before any effect. Missing, ambiguous, overlapping, mixed-encoding, or over-limit patches fail without mutation. The existing 1,048,576-byte MCP message cap remains the outer encoded request limit. `move` relocates one allowed source to one allowed, absent destination. `delete` permanently removes one allowed file or empty directory. It is not a trash, archive, retention, or undo operation. Directory-recursive deletion is unsupported.

An operation that changes existing content, relocates it, or removes it is bound to the caller's opaque current-source fingerprint. `write` requires exactly one explicit precondition variant: `absent` for creation or `fingerprint` for complete replacement. `edit` and `delete` require a source fingerprint. `move` requires a source fingerprint and an explicit absent-destination precondition. A stale, missing, changed, denied, non-empty, or conflicting target fails without a partial mutation. Successful `write`, `edit`, and `move` results return canonical safe metadata and the resulting fingerprint needed for a follow-on call; `delete` returns only canonical identity and the fact of permanent removal. Results never contain content, host paths, or raw filesystem identity. Follow-on `stat` and `resolve` calls observe the resulting identity and existence state.

Tool annotations describe reality: `stat` is read-only and non-destructive; none of the four mutation tools are read-only; and all four mutation tools are destructive. `write` and `edit` may replace content, `delete` removes it permanently, and `move` removes the source path even though its destination must be absent and the source value is preserved. Client-side confirmation is useful interaction safety but is not server authorization or a server-side write gate.

## Required Behavior

- Validate an entire request—paths, content or patch encoding, operation size, and preconditions—before a filesystem effect.
- Existing-file mutation, move, and delete reject a fingerprint that no longer identifies the target. A source race cannot silently overwrite or remove newer content.
- A successful write, edit, or move is atomically visible as its complete new state. Failure, cancellation, timeout, or process interruption leaves each affected target in either its complete prior state or a complete committed final state; no partial file is exposed. A failure after the filesystem commit may report an uncertain outcome and must not trigger an automatic replay.
- Delete has no recovery guarantee. Once success is returned, the gateway makes no restoration claim.
- Each call is bounded by normal time/input/result limits plus a mutation-content limit and reports sanitized success, conflict, denial, cancellation, timeout, and I/O outcomes.
- JSONL and SQLite telemetry records safe operation type, outcome, latency, and bounded counts without raw paths, destination names, content, fingerprints, or host identities.

## Scope And Exclusions

The feature covers allowed regular files and empty directories under the configured vault. `stat` fingerprints both kinds. `write` accepts UTF-8 text or explicitly base64-encoded bytes up to the fixed decoded-value limit. Each `edit` patch uses one explicit encoding consistently across all of its old/new replacement values and obeys the fixed operation, source, decoded-value, aggregate-patch, and result limits above; binary content is never inferred from an extension. `move` and `delete` may operate on allowed attachments as well as notes.

It does not add generic host-file access, shell execution, HTTP proxying, recursive delete, a background watcher, undo history, a vault-wide transaction, or identity-aware multi-user access control. It does not maintain Obsidian links, references, frontmatter, or application indexes after rename or edit unless a later requirement adds semantic maintenance.

## Acceptance Criteria

1. ChatGPT can discover `stat` plus the four named mutation tools through the authenticated `obsidian` connector. `stat` is accurately described as read-only and non-destructive; all mutation tools are non-read-only and destructive, including `move` because it removes the source path.
2. `stat` returns an opaque fingerprint for allowed existing regular files and directories. Valid create, complete replacement, bounded multi-replacement patch, move, and permanent delete calls change only explicit allowed vault targets and return canonical safe metadata that subsequent `stat` and `resolve` calls observe.
3. Existing-target writes, edits, moves, and deletes refuse stale fingerprints; absent-create and move-destination preconditions refuse collisions. Fixture races prove a rejected call changes neither affected path.
4. Absolute paths, traversal, hidden or denied segments, symlink escapes, disallowed kinds, non-empty directory deletion, invalid or mixed patch encodings, missing or ambiguous patch matches, overlapping replacements, and over-limit inputs fail closed without mutation or host-path disclosure.
5. Cancellation, timeout, injected I/O failure, hostile external replacement, and subprocess interruption tests exercise the production filesystem commit points and prove complete prior-or-final observable outcomes, collision-safe no-replace behavior, no content-bearing temporary residue, and descriptor cleanup. Permanent delete is not represented as recoverable, and an uncertain post-commit outcome is not automatically replayed.
6. The registration/schema, filesystem-confinement, telemetry, and authenticated-tunnel proof surfaces in `docs/TESTING.md` pass for the expanded tool set; local SDK testing alone does not establish live ChatGPT behavior.

## Forces And Decisions

| Force | Status | Requirement decision |
| --- | --- | --- |
| State and lifecycle | present | Each call has explicit precondition, changed, rejected, and terminal-error outcomes; the vault owns persisted state. |
| Persistence | absent | No mutation queue, approval ledger, trash, or undo store is introduced. |
| Contracts and validation | present | Validate paths, explicit value encodings, patch match uniqueness and non-overlap, sizes, and opaque source/absence preconditions before filesystem effects; contract-test the MCP boundary. |
| Internal typing | present | Keep operation kind, source/destination identity, value encoding, patch operations, and fingerprint/absence preconditions distinct. |
| Concurrency | present | Fingerprint-bound compare-and-change rejects source races rather than last-writer-wins. |
| Caching | absent | Mutation results reflect the filesystem effect just performed; no stale projection is authoritative. |
| Failure and resilience | present | Filesystem failure, cancellation, and timeout fail closed; no automatic retry replays an uncertain mutation. |
| Protocols and boundaries | present | MCP descriptor, annotations, telemetry, and authenticated tunnel agree on mutation semantics. |

## Decision Record

Eric chose permanent deletion, default exposure of all mutation tools, a separate read-only `stat` fingerprint tool, and a structured multi-replacement patch for `edit`. After platform grounding showed that macOS has no existing-name full-source-version compare-and-change primitive, Eric chose to preserve the no-silent-overwrite promise and recut the work instead of accepting the final-syscall race or adding a partial cooperative-writer boundary. The server adds no write-mode gate, undo layer, or purported per-message authorization proof. The safety boundary remains the narrow vault scope, exact fingerprint/absence preconditions, whole-patch validation, honest destructive metadata, and observable proof.

## Progressive Disclosure And Route

Read `docs/ARCHITECTURE.md`, `docs/obsidian.md`, and `docs/TESTING.md`; then read `internal/fsx/` for fd-anchored confinement/fingerprints, `internal/tools/obsidian/` for descriptor ownership, `internal/audit/` for safe telemetry, and the OpenAI Secure MCP Tunnel runbook for live proof.

This is broader than one implementation unit: filesystem mutation semantics, descriptor/telemetry changes, and authenticated connector proof require coordinated delivery. Issue #3 has an approved foundation/public-activation graph, but that graph is blocked by the feasibility gate above and must return to `scoping:phase-planning` before either child is treated as executable.
