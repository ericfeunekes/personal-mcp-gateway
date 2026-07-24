---
title: "Obsidian Native Document Reading"
status: draft
purpose: "Define native ChatGPT access to supported vault documents without widening the gateway into a generic file server."
covers:
  - internal/tools/obsidian/
  - internal/fsx/
  - internal/mcp/
  - docs/runbooks/openai-tunnel.md
---

# Obsidian Native Document Reading

## Outcome

An operator can ask `obsidian` to read a supported vault document and ChatGPT receives the original, correctly typed document for native handling. The gateway does not pretend that a text extraction is the document: native-file delivery is the primary result and extracted text is optional, bounded evidence only.

The support target is the current official OpenAI accepted-file list at https://developers.openai.com/api/docs/guides/file-inputs#full-list-of-accepted-file-types, subject to safely representable local vault forms. The server must not silently narrow that target to Markdown or claim a format works merely because a local parser can open it.

## Public Capability

`obsidian` adds `read_document`, a native document-reading capability distinct from Markdown `read`, `read_many`, and `grep`. It accepts one explicit vault-relative file path plus a bounded request shape and returns unaltered original bytes with an accurate media type through an MCP result form ChatGPT can consume natively. Markdown source-unit selection, graph links, and `grep` remain Markdown-specific.

The MCP content representation remains activation-gated: local-SDK and authenticated ChatGPT-through-tunnel proof must establish that the SDK, tunnel, and client carry the original as a native file. ChatGPT's built-in one-time consent to materialize a connector-returned file is an acceptable native security boundary; manual download, upload, or reattachment by the operator is not. A URL, local path, base64 text, or extraction is not equivalent evidence unless the live client demonstrably uses it as the original document.

## Required Behavior

- Document access uses explicit paths, root confinement, hidden-path/symlink denial, cancellation, timeout, and source-change safeguards equivalent to current read tools.
- Original bytes are unchanged. Returned media type comes from a bounded allowlist and validated file evidence; extension alone cannot make a trusted type claim.
- Malformed, unsupported, ambiguous, oversized, changed, or disallowed sources return a structured sanitized error and no partial or mislabelled artifact.
- Native transfer has a documented bound. The 64 KiB structured-result cap still governs metadata/text, but cannot truncate a document while calling the output native delivery.
- The current below-50-MB product target cannot use one atomic embedded-resource result: the exact 49,999,999-byte local SDK call breached both peak and 30-second retained-RSS limits. A resource-link or native-hosted mechanism must prove bounded backing-store lifecycle, byte identity, cleanup, tunnel/client consumption, and the same native ChatGPT experience before that size target is advertised.
- A format becomes advertised only after representative fixture and authenticated ChatGPT model-journey proof show native interpretation through the installed tunnel. Revalidate when the upstream accepted-file contract or transfer mechanism changes.
- Telemetry retains only safe format class, outcome, latency, and bounded byte counts—never bytes, paths, names, extracted content, or opaque identities.

## Scope And Exclusions

The target includes applicable local regular-file classes in the current official list: PDFs; spreadsheets and delimited data; rich documents; presentations; and accepted text/code formats. The original file is authoritative; parsing/OCR is not a prerequisite for a native result.

Google Docs/Sheets/Slides identifiers are not vault files and are excluded. Apple package formats and any directory-backed local representation require an explicit representation decision and compatibility proof before they are supported. Images, audio, video, archives, arbitrary opaque binaries, generic downloads, host-file serving, background indexing, and semantic document search are excluded.

## Acceptance Criteria

1. The server advertises document reading only after local SDK and authenticated ChatGPT-through-tunnel tests prove its result is received and used as the original native document, not a path, URL, truncated text, or base64 surrogate.
2. Every enabled local format class has fixtures proving byte identity from the allowed vault source to the MCP result and correct media-type labeling; malformed or spoofed files cannot obtain a trusted label.
3. A representative fixture from every enabled class is successfully interpreted in ChatGPT through the installed tunnel. Scanned PDFs additionally prove the claimed client-visible OCR/visual behavior; failure leaves that subtype unadvertised.
4. Denied paths, symlink escapes, hidden files, traversal, oversized files, malformed sources, unsupported forms, cancellation, timeout, and source changes fail closed without leaking host paths or partial document data.
5. Transfer leaves no startup scan, background index, persistent document copy, or raw-content telemetry. Resource and descriptor cleanup remain within the existing minimal-machine-impact proof framework.
6. `read`, `read_many`, `grep`, and graph operations retain their current Markdown-only semantics and acceptance behavior.

## Forces And Decisions

| Force | Status | Requirement decision |
| --- | --- | --- |
| State and lifecycle | deferred | Per-call source identity and transfer completion are required, but no catalog, ingestion job, or persistent processing state is introduced. |
| Persistence | absent | The vault remains authoritative; no document copy, extraction cache, or index exists. |
| Contracts and validation | present | Validate request, confined identity, format classification, size, and transfer result; test the native client boundary. |
| Internal typing | present | Keep format class, local representation, media type, fingerprint, and delivery result distinct. |
| Concurrency | present | Concurrent reads are independent; a changed source invalidates its own delivery rather than returning mixed bytes. |
| Caching | absent | Native delivery reads current source; stale extraction/cached bytes are not authority. |
| Failure and resilience | present | Unsupported transfer, malformed file, claimed OCR failure, cancellation, and timeout yield clear failure, not partial support. |
| Protocols and boundaries | present | Go MCP SDK, OpenAI Secure MCP Tunnel, and ChatGPT ingestion form one compatibility boundary requiring empirical proof. |

## Decision Record

Eric chose native ChatGPT handling of the original document over normalized text. Native artifact delivery is therefore non-negotiable and the transport representation is a feasibility gate, not an implementation assumption. On 2026-07-24, Eric accepted ChatGPT's built-in one-time file-materialization consent as part of the native experience because the model consumes the connector-returned artifact without an operator download, upload, or reattachment step, selected `read_document` as the stable public tool name, and selected the current OpenAI below-50-MB ceiling as the product size target. The full current OpenAI accepted-file surface is the target where it has a safe local vault representation; unproven forms remain visibly unsupported.

## Progressive Disclosure And Route

Read `docs/ARCHITECTURE.md`, `docs/obsidian.md`, `docs/TESTING.md`, OpenAI's accepted-file guidance, the Markdown handlers, `internal/fsx/`, and `docs/runbooks/openai-tunnel.md` before planning.

Small embedded PDF delivery passed the authenticated interaction boundary with the accepted one-time materialization consent. The exact 49,999,999-byte embedded-resource path failed the local peak and retained-RSS gates, so the 50 MB product target remains transport-gated on resource-link or native-hosted delivery. Issue #4 remains in Backlog pending that empirical boundary, `scoping:phase-planning`, and explicit business Priority.
