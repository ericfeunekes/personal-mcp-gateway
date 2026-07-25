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

An authorized operator using the personal `obsidian` MCP server can inspect mutation-ready identity, create, write, edit, move, and permanently delete allowed vault files. Operations are explicit, stateless, root-confined, bounded, and truthful about whether a change occurred. This trusted personal connector advertises mutation tools by default; it has neither a separate local write-mode switch nor a multi-user authorization system.

The vault remains the source of truth. The gateway maintains neither a shadow authority nor intended durable mutation state. A same-directory stage is transient non-authoritative recovery material; excluded loss of both owners or the system may strand it for external operator handling, but the gateway never interprets, restores, publishes, or replays it. Metadata-only audit data is evidence of an operation, not an undo log or a second authority.

## Locked Invariants

- **Vault confinement:** every public path is vault-relative and passes the existing hidden-path, traversal, symlink, and root-confinement boundary. No generic host filesystem, shell, or HTTP capability is introduced. Source: `AGENTS.md` project invariants and Eric's issue #3 continuation authorization.
- **Exact refusal contract:** every mutation carries an exact fingerprint or absence precondition; structured edit also requires exact context. Every mismatch detected at the required validation checkpoints refuses without changing a public target. Source: Eric's issue #3 continuation authorization.
- **Honest concurrency boundary:** the macOS external-writer race after final revalidation is accepted for this controlled workflow and must remain visible; the implementation and proof must not call it full CAS. Source: Eric's issue #3 continuation authorization and the platform evidence below.
- **Explicit recovery, no second authority:** implementation-private staging supports atomic whole-file visibility, and the tested single-owner interruption boundary must leave no content-bearing residue. Each gateway process may strand at most four non-authoritative 8 MiB stages per excluded double-owner/system loss event for external operator handling; no aggregate residue bound is claimed across repeated excluded failures or multiple gateway processes. There is no undo, trash, journal, replay, shadow authority, or durable mutation queue. Permanent delete has no recovery promise. Source: `AGENTS.md` state-ownership invariants, the established recovery contract, and Eric's issue #3 continuation authorization.
- **Fixed, narrow public surface:** the activated delta is read-only `stat` plus destructive `write`, structured `edit`, `move`, and permanent `delete`, with the fixed limits in this requirement. Source: approved issue #3/#6/#7 graph and Eric's continuation authorization.
- **Proof before activation:** issue #6 must prove the private filesystem/process foundation before issue #7 begins the public mutation surface. Issue #7 owns MCP and connector-facing disposable-fixture proof. Release, deployment, external settings changes, and the installed personal-vault destructive journey require separate authorization. Source: Eric's issue #3 continuation authorization and `docs/TESTING.md`.

## Authority And Concurrency Contract

The vault is the only content authority. Every mutation carries an exact
caller-observed precondition: absence for create and move destination, or the
opaque complete source fingerprint for replacement, edit, move source, and
delete. `edit` additionally carries exact replacement context. The gateway
opens the intended source through the confined descriptor boundary, validates
the complete request against that opened version, and revalidates the complete
source fingerprint and relevant destination absence immediately before the
namespace effect. If the intended target is missing or a mismatch is detected
at a required checkpoint, the patch context is missing or ambiguous, or a
destination already exists, the operation refuses without an effect.

On volumes that advertise the capability, macOS provides atomic
absent-destination enforcement through `renameatx_np(..., RENAME_EXCL)`;
unsupported volumes return `ENOTSUP` and operations requiring no-replace fail
closed. Every whole-file commit—create, replacement, and edit—uses a fully
written stage in the target directory and an atomic rename so observers see
either complete prior bytes or complete final bytes. Move uses exclusive rename
to an absent destination. Delete
revalidates immediately before `unlinkat` or `rmdir` and is permanent.

Create, replacement, and edit use a synchronous, short-lived commit helper as the
transient effect owner; this is not an always-on service or durable job. Before
the helper starts, the parent chooses one high-entropy implementation-private
stage name and establishes a control channel whose helper endpoint is the only
intentionally inherited copy; every unrelated descriptor and channel copy is
close-on-exec. The same gateway executable enters a private versioned helper
mode before ordinary config, audit, or app startup. It receives only the fixed
protocol and inherited confined root/parent/source descriptors used for stage
creation, final validation, and the namespace syscall—never a host path. The
helper creates the stage name exclusively, reports its raw stage identity before
writing caller bytes, and cleans it if parent liveness is lost before an
explicit commit token. Before that identity announcement, a surviving parent
may clean only its chosen high-entropy name after bounded nofollow
kind/owner/mode/link checks. After announcement, it additionally requires the
reported raw identity. Both cleanup paths revalidate at the last available
checkpoint; the final `unlinkat` retains a race with a deliberately targeting
local writer, which is outside the private-stage threat boundary rather than
hidden as conditional unlink. The helper itself performs the final source
fingerprint and destination-absence revalidation and immediately issues the
namespace syscall; parent-side validation is preparatory, not the claimed final
checkpoint. Once the commit token is received it wins over later parent EOF, so
the helper either completes the atomic effect and cleanup or leaves the caller
with an uncertain prior-or-final outcome. The fixed internal protocol exposes no
public tool, path, or generic host capability and must bound concurrent helpers.

Each stage is an implementation-private high-entropy name in the target
directory, mode `0600`, opened nofollow, regular, single-link, and
identity-checked. At most four helpers and four stages may exist concurrently,
each stage is at most 8 MiB, and private control frames are at most 4 KiB.
Helper execution shares the existing two-second tool deadline and cleanup is
expected within that same healthy-scheduler bound. The gateway performs no
startup or vault-wide staging scan.

This is optimistic concurrency, not kernel compare-and-swap. macOS exposes no
public namespace primitive that binds a caller-supplied complete source-version
stamp to replacement, move, or removal of an existing name. An external writer
can therefore change the source after the final revalidation and before the
namespace syscall. Eric accepts that narrow residual race for this controlled
personal Obsidian workflow. The gateway must not describe the contract as full
CAS, must not claim that a writer winning that unobservable window is always
preserved, and must not weaken or omit any validation that can occur before the
effect. Advisory locks and cooperating-writer assumptions are not part of the
authority boundary.

Atomicity here means public namespace visibility of complete prior or final
bytes. The contract makes no `fsync`, `F_FULLFSYNC`, crash-consistency, or
power-loss namespace-durability guarantee.

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

`stat` returns canonical safe metadata plus an opaque current-source fingerprint for one existing allowed regular file or empty directory. It is the public fingerprint-acquisition path for attachments and empty directories; `read` continues returning the same 43-character unpadded base64url fingerprint with Markdown content. Directory `stat` reads at most one entry to prove emptiness, binds device/inode/size plus nanosecond modification/change timestamps, and rejects a non-empty directory. Final move/delete revalidation repeats the same at-most-one-entry emptiness check and source stamp before the namespace effect. Supported-volume tests must prove add/remove membership races are detected at that checkpoint; the contract does not claim a collision-free arbitrary-directory membership version. `stat` does not return content, follow symlinks, or expose raw filesystem identity.

`write` creates a file or replaces one complete file value of at most 524,288 decoded bytes. `edit` applies one atomic structured patch to one existing regular file of at most 8,388,608 bytes and produces a file no larger than that same limit. A patch contains 1 through 64 ordered replacement operations. Each replacement carries a non-empty `old` value and a `new` value using one patch-wide explicit `utf8` or `base64` encoding; each decoded value is at most 524,288 bytes, and all decoded old/new values together are at most 524,288 bytes. Every `old` value must match exactly once in the same fingerprinted original source, replacement spans must not overlap, and the entire patch is validated against that original source before any effect. Missing, ambiguous, overlapping, mixed-encoding, or over-limit patches fail without mutation. The existing 1,048,576-byte MCP message cap remains the outer encoded request limit. `move` relocates one allowed source to one allowed, absent destination. `delete` permanently removes one allowed file or empty directory. It is not a trash, archive, retention, or undo operation. Directory-recursive deletion is unsupported.

An operation that changes existing content, relocates it, or removes it is bound to the caller's opaque current-source fingerprint. `write` requires exactly one explicit precondition variant: `absent` for creation or `fingerprint` for complete replacement. `edit` and `delete` require a source fingerprint. `move` requires a source fingerprint and an explicit absent-destination precondition. A stale, missing, changed, denied, non-empty, or conflicting target fails without a partial mutation. Successful `write`, `edit`, and `move` results return canonical safe metadata and the resulting fingerprint needed for a follow-on call; `delete` returns only canonical identity and the fact of permanent removal. Results never contain content, host paths, or raw filesystem identity. Follow-on `stat` and `resolve` calls observe the resulting identity and existence state.

Tool annotations describe reality: every tool is closed-world; `stat` is read-only, non-destructive, and idempotent; none of the four mutation tools are read-only; and all four mutation tools are destructive and precondition-idempotent. `write` and `edit` may replace content, `delete` removes it permanently, and `move` removes the source path even though its destination must be absent and the source value is preserved. Idempotence means replaying the exact successful precondition cannot apply a second effect; it does not authorize automatic replay after an uncertain outcome. Client-side confirmation is useful interaction safety but is not server authorization or a server-side write gate.

## Required Behavior

- Validate an entire request—paths, content or patch encoding, operation size, and preconditions—before a filesystem effect.
- Existing-file mutation, move, and delete revalidate the complete fingerprint at the last available pre-effect checkpoint and reject every mismatch observed there. Exact edit context is validated against that same fingerprinted source. The accepted external-writer window after revalidation is the only weaker boundary and is reported as such.
- A successful write, edit, or move is atomically visible as its complete new state. Failure, cancellation, timeout, or process interruption leaves each public target in either its complete prior state or a complete committed final state; no partial public file is exposed. A failure after the filesystem commit may report an uncertain outcome and must not trigger an automatic replay.
- Whole-file staging is implementation-private and inaccessible through the public tool path grammar. Ordinary errors, cancellation, timeout, and the tested single-owner interruption boundary—SIGKILL of either parent or helper while the other remains—leave no content-bearing staging residue after healthy-scheduler quiescence. Cleanup uses the chosen-name or announced-identity checks above at the last available checkpoint; it does not claim conditional unlink against a deliberately targeting writer. Sequential or simultaneous loss of both owners, process-group loss, OS crash, power loss, and that deliberately targeting writer are outside the residue guarantee. Each affected gateway process may strand up to four 8 MiB non-authoritative stages per excluded event for external operator handling; repeated excluded failures or multiple processes have no claimed aggregate bound. The gateway keeps no undo, trash, journal, replay, or shadow authority.
- Delete has no recovery guarantee. Once success is returned, the gateway makes no restoration claim.
- Each call is bounded by normal time/input/result limits plus a mutation-content limit and reports sanitized success, conflict, denial, cancellation, timeout, and I/O outcomes.
- JSONL and SQLite telemetry records safe operation type, outcome, latency, and bounded counts without raw paths, destination names, content, fingerprints, or host identities.

## Scope And Exclusions

The feature covers allowed regular files and empty directories under the configured vault. `stat` fingerprints both kinds. `write` accepts UTF-8 text or explicitly base64-encoded bytes up to the fixed decoded-value limit. Each `edit` patch uses one explicit encoding consistently across all of its old/new replacement values and obeys the fixed operation, source, decoded-value, aggregate-patch, and result limits above; binary content is never inferred from an extension. `move` and `delete` may operate on allowed attachments as well as notes.

It does not add generic host-file access, shell execution, HTTP proxying, recursive delete, a background watcher, undo history, a vault-wide transaction, or identity-aware multi-user access control. It does not maintain Obsidian links, references, frontmatter, or application indexes after rename or edit unless a later requirement adds semantic maintenance.

## Acceptance Criteria

1. ChatGPT can discover `stat` plus the four named mutation tools through the authenticated `obsidian` connector. `stat` is accurately described as read-only and non-destructive; all mutation tools are non-read-only and destructive, including `move` because it removes the source path.
2. `stat` returns an opaque fingerprint for allowed existing regular files and empty directories. Valid create, complete replacement, bounded multi-replacement patch, move, and permanent delete calls change only explicit allowed vault targets and return canonical safe metadata that subsequent `stat` and `resolve` calls observe.
3. Existing-target writes, edits, moves, and deletes refuse stale fingerprints observed at the final pre-effect checkpoint; absent-create and move-destination preconditions refuse collisions. Fixture races before that checkpoint prove a rejected call changes neither affected path. Tests and documentation separately name the accepted external-writer window between final revalidation and the namespace syscall rather than presenting these operations as full CAS.
4. Absolute paths, traversal, hidden or denied segments, symlink escapes, disallowed kinds, non-empty directory deletion, invalid or mixed patch encodings, missing or ambiguous patch matches, overlapping replacements, and over-limit inputs fail closed without mutation or host-path disclosure.
5. Cancellation, timeout, injected I/O failure, hostile external replacement before final revalidation, and subprocess interruption tests exercise the production filesystem commit points and prove complete prior-or-final public outcomes, collision-safe no-replace behavior, symmetric parent/helper cleanup at the stated single-owner interruption boundary, no content-bearing staging residue after healthy-scheduler quiescence, and descriptor cleanup. Permanent delete is not represented as recoverable, and an uncertain post-commit outcome is not automatically replayed. Proof does not claim to eliminate the accepted final-syscall race or cover simultaneous owner loss, process-group loss, OS crash, or power-loss durability.
6. The registration/schema, filesystem-confinement, telemetry, and authenticated-tunnel proof surfaces in `docs/TESTING.md` pass for the expanded tool set; local SDK testing alone does not establish live ChatGPT behavior.

## Forces And Decisions

| Force | Status | Requirement decision |
| --- | --- | --- |
| State and lifecycle | present | Whole-file commit has one request-scoped ownership handshake: stage announced, payload complete, commit authorized, then committed-or-cleaned. It has no durable job or recovery queue; the vault owns persisted state. |
| Persistence | present | The vault is the sole durable content store. Implementation-private staging must not survive the tested interruption boundary; no queue, approval ledger, trash, undo store, or shadow authority is introduced. |
| Contracts and validation | present | Validate paths, explicit value encodings, patch match uniqueness and non-overlap, sizes, and opaque source/absence preconditions before filesystem effects; contract-test the MCP boundary. |
| Internal typing | present | Keep operation kind, source/destination identity, value encoding, patch operations, and fingerprint/absence preconditions distinct. |
| Concurrency | present | Exact fingerprint, absence, and patch-context checks reject every mismatch observed through the final pre-effect checkpoint. The macOS final-syscall external-writer window is accepted and explicitly not modeled as full compare-and-swap. |
| Caching | absent | Mutation results reflect the filesystem effect just performed; no stale projection is authoritative. |
| Failure and resilience | present | Filesystem failure, cancellation, and timeout fail closed; no automatic retry replays an uncertain mutation. |
| Protocols and boundaries | present | MCP descriptor, annotations, telemetry, and authenticated tunnel agree on mutation semantics. |

## Decision Record

Eric chose permanent deletion, default exposure of all mutation tools, a separate read-only `stat` fingerprint tool, and a structured multi-replacement patch for `edit`. After platform grounding showed that macOS has no existing-name full-source-version compare-and-change primitive, Eric accepted the narrow external-writer race after final revalidation for this controlled Obsidian workflow. Every mutation still requires exact fingerprint or absence preconditions, `edit` requires exact patch context, and every observed mismatch refuses safely. The server adds no write-mode gate, undo layer, trash, shadow authority or intended durable mutation state, cooperative-writer requirement, or purported per-message authorization proof. The safety boundary is the narrow vault scope, last-available pre-effect validation, atomic whole-file visibility/no-replace where supported, honest destructive metadata, explicit staging recovery, and proof that does not overstate the platform guarantee.

## Progressive Disclosure And Route

Read `docs/ARCHITECTURE.md`, `docs/obsidian.md`, and `docs/TESTING.md`; then read `internal/fsx/` for fd-anchored confinement/fingerprints, `internal/tools/obsidian/` for descriptor ownership, `internal/audit/` for safe telemetry, and the OpenAI Secure MCP Tunnel runbook for live proof.

This remains the approved two-unit delivery graph under issue #3: issue #6 owns the confined filesystem mutation foundation and its recovery/race proof; issue #7 owns the public descriptors, handlers, telemetry, exact tool-set proof, and connector-facing disposable-fixture proof. Issue #7 may start only after issue #6 proves the authority and recovery contract above.
