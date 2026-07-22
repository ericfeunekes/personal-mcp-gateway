---
title: "Issue 4 Native Document Transfer Spike"
status: blocked-local-resource-proof
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

## Outcome on 2026-07-21

The candidate never reached installation, so no authenticated ChatGPT transfer
test ran and the accepted five-tool metadata never changed.

After canonical tests passed, two consecutive exact-candidate resource smokes
failed the then-current single-batch local release gate:

- Run one exceeded the 8 MiB post-30-second RSS-growth limit: 12,521,472 bytes.
- Run two passed RSS but exceeded the 256 KiB heap-allocation-growth limit:
  277,624 bytes, or 15,480 bytes over the bound.

Both runs passed every other reported CPU, high-water RSS, FD recovery, SDK
result, latency, scan, cancellation, concurrency, descriptor-stability, and
60-second idle invariant. The release transaction remained clear after each
failure. The canonical checkout's LaunchAgent wrapper was reinstalled and read
back as loaded, tunnel-live, and tunnel-ready with the previously accepted
runtime.

The probe remains blocked before live proof. Resource-report schema v7 now
keeps the same 256 KiB retained-heap, 8 MiB retained-RSS, and 64 MiB lifetime
high-water limits while requiring a heap or RSS breach to persist in at least
two post-GC batches. RSS uses the maximum of each immediate, five-second, and
30-second stabilization window. The repaired gate still requires current-state
canonical and exact-candidate proof before another release attempt; do not
infer ChatGPT compatibility from the local SDK evidence.

## Outcome on 2026-07-22

The retained-growth gate repair is committed in `a3d6c28`, with its production
build correction in `4bd91e1`. Canonical tests, the production smoke build,
focused retained-growth tests, release-script tests, and independent gate
reviews passed. The repaired schema-v7 decision keeps the existing numeric
limits and requires a retained heap or RSS breach in at least two of three
post-GC batches.

The final predeclared current-state sequence did not clear release. All three
exact-candidate observations used the same candidate SHA-256
`8df896c95dd2b30c2184776e685cfbc223d66c37534bfd2e8ccda0252099cda5` and
passed the complete v7 resource report. Retained heap growth was 187,400,
187,664, and 198,192 bytes; retained RSS-window growth was 3,895,296,
2,912,256, and 2,703,360 bytes. All three also recovered file descriptors,
kept descriptors unchanged, showed zero idle CPU growth, and passed the
workload, latency, concurrency, boundary, and lifetime high-water checks.

All three control slots failed before emitting a resource report. The control
overlay changed the expected descriptor count from six to five, but the
committed candidate-specific smoke also requires the exact six-tool name set,
including the probe descriptor. The five-tool control therefore could not
satisfy that smoke contract. These failures are harness-contract failures,
not evidence of a control resource regression. The predeclared proof contract
forbade another replacement sequence after this setup failure, so the three
candidate passes cannot be used alone as release clearance.

No canonical release or activation was attempted, no authenticated ChatGPT
probe ran, and the accepted five-tool live surface was never changed. Issue #4
remains blocked on a valid paired control/candidate resource proof. Any future
attempt must first make the control mode explicit throughout the smoke grammar
and workload rather than changing only the descriptor count; it must then use
a newly reviewed proof contract before release.
