---
title: "Obsidian Mutation Tools"
status: draft
purpose: "Define the trusted personal-vault mutation contract for the obsidian MCP server."
covers:
  - internal/tools/obsidian/
  - internal/fsx/
  - internal/mcp/
  - internal/audit/
---

# Obsidian Mutation Tools

## Outcome

An authorized operator using the personal `obsidian` MCP server can create, write, edit, move, and permanently delete allowed vault files. Operations are explicit, stateless, root-confined, bounded, and truthful about whether a change occurred. This trusted personal connector advertises mutation tools by default; it has neither a separate local write-mode switch nor a multi-user authorization system.

The vault remains the source of truth. The gateway owns neither a shadow copy nor durable mutation state. Metadata-only audit data is evidence of an operation, not an undo log or a second authority.

## Public Capability

The `obsidian` server adds first-class `write`, `edit`, `move`, and `delete` tools alongside discovery and read tools. They never accept an absolute host path, shell expression, arbitrary URL, or server-side current directory. Every target and destination is an explicit vault-relative path and passes the same hidden-path, traversal, symlink, and root-confinement rules as reads.

`write` creates a file or replaces one complete file value. `edit` applies a bounded, deterministic change to one existing file. `move` relocates one allowed source to one allowed, absent destination. `delete` permanently removes one allowed file or empty directory. It is not a trash, archive, retention, or undo operation. Directory-recursive deletion is unsupported.

An operation that changes existing content, relocates it, or removes it is bound to the caller's opaque current-source fingerprint. Creation and a move destination require explicit absence preconditions. A stale, missing, changed, denied, non-empty, or conflicting target fails without a partial mutation. Successful results return only safe metadata needed for a follow-on call; never content, host paths, or raw filesystem identity.

Tool annotations describe reality: none are read-only; `delete` is destructive, and any operation that can replace or discard content carries the corresponding destructive hint. Client-side confirmation is useful interaction safety but is not server authorization.

## Required Behavior

- Validate an entire request—paths, content encoding, operation size, and preconditions—before a filesystem effect.
- Existing-file mutation, move, and delete reject a fingerprint that no longer identifies the target. A source race cannot silently overwrite or remove newer content.
- A successful write, edit, or move is atomically visible as its complete new state. Failure, cancellation, timeout, or process interruption leaves each affected target in either its complete prior state or the reported final state; no partial file is exposed.
- Delete has no recovery guarantee. Once success is returned, the gateway makes no restoration claim.
- Each call is bounded by normal time/input/result limits plus a mutation-content limit and reports sanitized success, conflict, denial, cancellation, timeout, and I/O outcomes.
- JSONL and SQLite telemetry records safe operation type, outcome, latency, and bounded counts without raw paths, destination names, content, fingerprints, or host identities.

## Scope And Exclusions

The feature covers allowed regular files and empty directories under the configured vault. `write` and `edit` accept bounded UTF-8 text or explicitly encoded bounded bytes; binary content is never inferred from an extension. `move` and `delete` may operate on allowed attachments as well as notes.

It does not add generic host-file access, shell execution, HTTP proxying, recursive delete, a background watcher, undo history, a vault-wide transaction, or identity-aware multi-user access control. It does not maintain Obsidian links, references, frontmatter, or application indexes after rename or edit unless a later requirement adds semantic maintenance.

## Acceptance Criteria

1. ChatGPT can discover the four named mutation tools through the authenticated `obsidian` connector, and their schemas/annotations accurately distinguish read-only from destructive behavior.
2. Valid create, complete replacement, bounded edit, move, and permanent delete calls change only explicit allowed vault targets and return canonical safe metadata that a subsequent `resolve` observes.
3. Existing-target writes, edits, moves, and deletes refuse stale fingerprints; absent-create and move-destination preconditions refuse collisions. Fixture races prove a rejected call changes neither affected path.
4. Absolute paths, traversal, hidden or denied segments, symlink escapes, disallowed kinds, non-empty directory deletion, invalid encodings, and over-limit inputs fail closed without mutation or host-path disclosure.
5. Cancellation, timeout, injected I/O failure, and interrupted-write tests prove atomic observable outcomes and descriptor cleanup. Permanent delete is not represented as recoverable.
6. The registration/schema, filesystem-confinement, telemetry, and authenticated-tunnel proof surfaces in `docs/TESTING.md` pass for the expanded tool set; local SDK testing alone does not establish live ChatGPT behavior.

## Forces And Decisions

| Force | Status | Requirement decision |
| --- | --- | --- |
| State and lifecycle | present | Each call has explicit precondition, changed, rejected, and terminal-error outcomes; the vault owns persisted state. |
| Persistence | absent | No mutation queue, approval ledger, trash, or undo store is introduced. |
| Contracts and validation | present | Validate paths, encodings, sizes, and opaque source/absence preconditions before filesystem effects; contract-test the MCP boundary. |
| Internal typing | present | Keep operation kind, source/destination identity, encoding, and preconditions distinct. |
| Concurrency | present | Fingerprint-bound compare-and-change rejects source races rather than last-writer-wins. |
| Caching | absent | Mutation results reflect the filesystem effect just performed; no stale projection is authoritative. |
| Failure and resilience | present | Filesystem failure, cancellation, and timeout fail closed; no automatic retry replays an uncertain mutation. |
| Protocols and boundaries | present | MCP descriptor, annotations, telemetry, and authenticated tunnel agree on mutation semantics. |

## Decision Record

Eric chose permanent deletion and default exposure of all mutation tools for this trusted personal gateway. The server adds no write-mode gate, undo layer, or purported per-message authorization proof. The safety boundary is the narrow vault scope, exact preconditions, honest destructive metadata, and observable proof.

## Progressive Disclosure And Route

Read `docs/ARCHITECTURE.md`, `docs/obsidian.md`, and `docs/TESTING.md`; then read `internal/fsx/` for fd-anchored confinement/fingerprints, `internal/tools/obsidian/` for descriptor ownership, `internal/audit/` for safe telemetry, and the OpenAI Secure MCP Tunnel runbook for live proof.

This is broader than one implementation unit: filesystem mutation semantics, descriptor/telemetry changes, and authenticated connector proof require coordinated delivery. Issue #3 remains in Backlog pending `scoping:phase-planning` and explicit business Priority.
