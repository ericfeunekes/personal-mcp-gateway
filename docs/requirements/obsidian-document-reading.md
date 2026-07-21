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

`obsidian` adds a named document-reading capability distinct from Markdown `read`, `read_many`, and `grep`. It accepts one explicit vault-relative file path plus a bounded request shape and returns unaltered original bytes with an accurate media type through an MCP result form ChatGPT can consume natively. Markdown source-unit selection, graph links, and `grep` remain Markdown-specific.

The actual tool name and MCP content representation are activation-gated: local-SDK and authenticated ChatGPT-through-tunnel proof must establish that the SDK, tunnel, and client carry the original as a native file. A URL, local path, base64 text, or extraction is not equivalent evidence unless the live client demonstrably uses it as the original document.

## Required Behavior

- Document access uses explicit paths, root confinement, hidden-path/symlink denial, cancellation, timeout, and source-change safeguards equivalent to current read tools.
- Original bytes are unchanged. Returned media type comes from a bounded allowlist and validated file evidence; extension alone cannot make a trusted type claim.
- Malformed, unsupported, ambiguous, oversized, changed, or disallowed sources return a structured sanitized error and no partial or mislabelled artifact.
- Native transfer has a documented bound. The 64 KiB structured-result cap still governs metadata/text, but cannot truncate a document while calling the output native delivery.
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

Eric chose native ChatGPT handling of the original document over normalized text. Native artifact delivery is therefore non-negotiable and the transport representation is a feasibility gate, not an implementation assumption. The full current OpenAI accepted-file surface is the target where it has a safe local vault representation; unproven forms remain visibly unsupported.

## Progressive Disclosure And Route

Read `docs/ARCHITECTURE.md`, `docs/obsidian.md`, `docs/TESTING.md`, OpenAI's accepted-file guidance, the Markdown handlers, `internal/fsx/`, and `docs/runbooks/openai-tunnel.md` before planning.

The native-transfer mechanism is an empirical feasibility boundary and the format matrix is broader than one delivery loop. Issue #4 remains in Backlog pending `scoping:phase-planning`, a transfer spike, and explicit business Priority.
