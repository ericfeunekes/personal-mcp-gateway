---
title: "Issue 4 Native Document Transfer Spike"
status: pending-live-proof
issue: 4
---

# Issue 4 Native Document Transfer Spike

This disposable branch tests one question before the document-reading design is
phased: does authenticated ChatGPT interpret a PDF returned as MCP embedded
resource bytes, without a download, upload, attachment, or other surrogate
step?

The pending candidate advertises exactly one temporary additional read-only
tool, `document_transfer_probe`. It accepts no arguments, reads no vault data,
and returns one synthetic `application/pdf` resource plus only its MIME type and
raw byte count as structured metadata. The PDF contains the live verifier's
nonce and visual relationship; neither appears in the descriptor or structured
metadata.

## Local evidence required before release

- The fixture is a valid, renderable one-page PDF.
- Extracted text contains the byte-only nonce, and rendered visual inspection
  confirms the intended shape relationship.
- The exact built candidate advertises six tools and returns the fixture with
  its URI, MIME type, PDF boundaries, and byte-only nonce unchanged.
- A valid PDF exactly at the 512 KiB raw-byte boundary is accepted, while a
  valid PDF one byte larger is rejected before transfer.
- The canonical suite and exact candidate smoke/resource gates pass.

The 512 KiB raw limit keeps base64 plus the MCP JSON envelope below the
gateway's 1 MiB stdio message bound. It is spike evidence, not an accepted
document-reading product limit.

## Live verdict

Pass only when a fresh authenticated ChatGPT model run calls the probe and
states both the nonce and visual relationship directly from the returned PDF.
A successful tool call alone, a visible resource or download, or any extra
download/upload/attachment step is a failure. Ordinary connector approval to
invoke the read-only tool is allowed.

Every live outcome is followed by exact release rollback and metadata refresh
to restore the accepted `grep`, `ls`, `read`, `read_many`, and `resolve` surface.
The candidate's pinned release controller mechanically refuses `accept` and
prints only the exact rollback command while the transaction is pending.
On pass, issue #4 returns to whole-design phase planning. On failure, ignore,
download-only handling, or surrogate handling, the issue records a client or
upstream blocker; no fallback transport is introduced.
