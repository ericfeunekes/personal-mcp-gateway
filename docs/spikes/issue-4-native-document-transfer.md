---
title: "Issue 4 Native Document Transfer Spike"
status: pdf-production-candidate
issue: 4
---

# Issue 4 Native Document Transfer Spike

This record preserves the disposable feasibility experiments and the evidence
that led to the production PDF activation slice. The temporary
`document_transfer_probe` implementation has been removed. The current
candidate exposes the stable `read_document` tool only in a
`pdf_candidate` build and reads explicit, confined vault PDFs through the
production capture, validation, handoff, and transport path.

## Local evidence required before release

- The fixture is a valid, renderable one-page PDF.
- Extracted text contains the byte-only nonce, and rendered visual inspection
  confirms the intended shape relationship.
- The exact built candidate advertises six tools and returns the fixture with
  its URI, MIME type, PDF boundaries, and byte-only nonce unchanged.
- A valid PDF exactly at the 512 KiB raw-byte boundary is accepted, while a
  valid PDF one byte larger is rejected before transfer.
- The canonical suite and exact candidate smoke/resource gates pass.

The historical 512 KiB limit below belongs only to the disposable probe. The
production PDF candidate limit is 49,999,999 raw bytes.

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

## Final outcome on 2026-07-22

The control-path blocker was fixed in `f1ddc07` by adding an explicit
schema-v8 `accepted` resource surface. Canonical release proof remains the
default six-tool `candidate` surface; `--resource-control` is valid only with
`--resource-json`, requires exactly the accepted five descriptors, and is
rejected by canonical report-set validation.

A new mechanically ordered proof sequence ran once against the frozen
`f1ddc07` smoke tree. All three five-tool controls and all three six-tool
candidates passed. The controls reported retained heap growth from 201,080 to
223,912 bytes and retained RSS-window growth from 2,895,872 to 3,420,160 bytes.
The candidates reported retained heap growth from 194,136 to 220,504 bytes and
retained RSS-window growth from 2,752,512 to 4,595,712 bytes. Every report also
passed lifetime high-water, FD recovery, descriptor stability, idle, workload,
latency, concurrency, and boundary checks.

The one canonical release entered pending state as release
`fb47eddffd4153b4276ebe43bd70f911a358d42838b7e01cfb233f09d96be8a9`.
An initial fresh ChatGPT conversation observed cached five-tool metadata and
correctly refused the unavailable probe. That was a live non-pass outcome under
the predeclared rule, but the release was not rolled back immediately. Instead,
the connector metadata was refreshed under the same pending release. This was
a protocol deviation and the later result cannot retroactively make the strict
spike verdict pass.

After the refresh showed `document_transfer_probe`, a second fresh authenticated
conversation called it exactly once. ChatGPT then requested a separate built-in
approval to materialize one returned file attachment. After approval, ChatGPT
read the PDF itself without any user download, upload, or reattachment and
reported the byte-only nonce
`NDX-7Q4M-9K2P-R8VC` and the visual relationship: a blue circle is left of an
orange square, horizontally aligned, separated, and non-overlapping. This proves
that ChatGPT can interpret the connector-returned PDF after its internal
materialization handoff. It does not satisfy the frozen pass criterion, which
allowed ordinary connector invocation approval but rejected an additional
attachment step.

The exact pending release was rolled back immediately. Release state read back
as clear, the canonical LaunchAgent and tunnel passed live verification, and a
second metadata refresh showed exactly the accepted `grep`, `ls`, `read`,
`read_many`, and `resolve` tools with no probe. The experiment therefore closes
the transport-feasibility uncertainty but does not pass its strict interaction
contract. Issue #4 remains open at a decision boundary: accept ChatGPT's
built-in one-time materialization consent as the intended native experience and
return to whole-design phase planning, or treat that extra consent as an
upstream client limitation. The temporary probe must not be accepted or reused
as the product implementation.

## Product decision on 2026-07-24

Eric accepted ChatGPT's built-in one-time file-materialization consent as a
reasonable native security boundary. The accepted experience still forbids any
operator download, upload, or reattachment step. With that interaction decision
settled, the observed authenticated model interpretation closes the transport
feasibility boundary and issue #4 returns to whole-design phase planning. The
earlier proof-protocol deviations remain recorded above and are not rewritten as
a strict pass of that frozen experiment.

## Near-ceiling embedded-resource outcome on 2026-07-24

Eric selected the current OpenAI below-50-MB per-file ceiling as the product
target. A separately challenged local capacity spike therefore generated and
validated an exact 49,999,999-byte two-page PDF whose nonce and unlabeled visual
evidence were physically within the final 761 bytes. The frozen candidate was
commit `d9ff21c17c7d6ace0c723bac35e928cedfea47e2`, binary SHA-256
`7d4843d67d4854bc9a81f90a1d8d8d933577d1b85a4a1e48546ac4f31c64cdc0`,
and fixture SHA-256
`935d656fc945ea0c002af42af4ae62edfbf48f22aeb03872c7a4b51fe6beb69d`.
`pdfinfo`, page-specific `pdftotext`, and a rendered final-page geometry check
all passed before measurement.

The one exact SDK call transferred all 49,999,999 raw bytes unchanged in one
66,666,915-byte JSON-RPC response frame and returned in 1.167 seconds. The
ordinary structured result remained 52 bytes. File descriptors, activity
quiescence, the same-session follow-up, retained heap, and the exact two-call
60-second idle gate all passed.

The embedded path failed the existing minimal-machine-impact limits. Gateway
lifetime high-water RSS rose 384,262,144 bytes above the aligned baseline,
against a 64 MiB limit. RSS remained about 384.2 MB above baseline at the
immediate, five-second, and 30-second post-call checkpoints, against the 8 MiB
retained-RSS limit, even after acknowledged blocking GC. Retained Go heap grew
only 37,592 bytes, and RSS later fell below the original baseline during the
60-second idle window; this does not erase either measured breach.

Under the predeclared stop rule, the near-ceiling embedded-resource path is a
local no-go. No canonical release, connector activation, metadata refresh, or
authenticated ChatGPT attempt ran. This result does not reject a resource-link
or native-hosted delivery mechanism that streams from bounded backing storage;
that alternative remains an empirical design gate before the 50 MB product
target can be phased.

## Resource-link/native-hosted investigation on 2026-07-25

The pinned Go MCP SDK does not supply a non-atomic native-file transport behind
`ResourceLink`. `ResourceLink` serializes URI, name, MIME, size, and display
metadata only; it has no bytes, fetch callback, authorization, expiry, or
cleanup contract. The SDK's explicit `resources/read` response carries binary
content as one `Blob []byte`, which JSON encodes as one base64 value. Reading a
resource through that method therefore returns to the atomic representation
already rejected at the selected ceiling rather than creating a streaming
escape hatch.

Current official OpenAI Secure MCP Tunnel documentation describes forwarding
MCP JSON-RPC requests and responses between OpenAI and a reachable local stdio
or HTTP MCP server. It does not promise that ChatGPT dereferences a tool-result
resource link, expose URI authorization or expiry semantics, or define a
native-hosted/file-reference output lifecycle. The adjacent ChatGPT component
file APIs mention files returned by tool file references, but do not define how
an MCP `ResourceLink` becomes that file reference or how a private local source
is fetched through the Secure MCP Tunnel.

This closes the pinned SDK's resource-link branch, not every local transport
design. The architecture permits a narrow protocol contingency when the SDK
blocks a proven compatibility requirement. A remaining local spike must test
whether the gateway can fully capture and revalidate the source in bounded
anonymous memory, then incrementally base64-serialize the same embedded-resource
wire shape without constructing a second base64 string or whole JSON frame.
Capture must finish before emission so a changed source cannot leave a partial
artifact; direct descriptor-to-client streaming is not acceptable. Production
`read_document` implementation and phase-graph freeze remain gated on that
capacity/correctness result and, if local proof passes, authenticated tunnel
proof. A small authenticated resource-link probe remains a later alternative;
it requires a temporary release and metadata refresh and would not by itself
solve the atomic ceiling. No external mutation ran during this investigation.

## PDF production-candidate outcome on 2026-07-25

The narrow contingency is now implemented rather than inferred. `read_document`
opens one explicit confined PDF, admits its observed size against a weighted
49,999,999-byte process budget, copies it into one anonymous mapping, validates
the captured bytes, revalidates the source, and registers a random one-shot
payload. The marker expires after five seconds and is consumed only by the
native stdio/HTTP writers. The writers incrementally base64-encode the original
snapshot under a 30-second write deadline and never construct the SDK's atomic
`Blob []byte` result. Native JSON-RPC batches are rejected before dispatch;
ordinary messages still use the SDK implementation unchanged.

The first exact production-path run found that in-process `pdfcpu` validation
left about 101 MB of reclaimable heap pages resident and correctly failed the
retained-RSS and high-water gates. Moving validation to a bytes-only helper
cleared retained RSS, but mapping the helper input duplicated the 50 MB resident
set and still failed lifetime high-water. The helper now streams stdin into a
mode-private temporary file that is unlinked before bytes are written, validates
that anonymous backing, and exits. It accepts no path or host capability. The
next exact run returned all three 49,999,999-byte stdio resources with SHA-256
`935d656fc945ea0c002af42af4ae62edfbf48f22aeb03872c7a4b51fe6beb69d`,
`application/pdf`, correct terminal evidence, and maximum call latency 0.586
seconds. Gateway high-water delta was 50,438,144 bytes, retained RSS growth was
520,192 bytes, retained heap growth was 18,488 bytes, and every FD, quiescence,
follow-up, and 60-second idle observation passed. That run's aggregate verdict
was false only because the idle harness still expected the historical one
document call plus follow-up; the production sequence now makes three document
calls plus follow-up, and the harness expectation has been corrected to four.

The report was then strengthened to schema v3. Its HTTP half now launches the
built candidate's `http` command and connects through `/mcp`; it no longer
reconstructs an in-process handler. The report also measures the bytes-only
validator helper directly and combines its waited high-water RSS with the
larger stdio/HTTP gateway high-water. A first v3 run measured a conservative
126,066,688-byte upper bound (66,281,472-byte gateway high-water plus
59,785,216-byte validator high-water) and exposed only that an already-closing
readiness connection could leave the post-call HTTP FD count lower than the
baseline. The gate now treats no increase as recovery. A further proof review
made the HTTP candidate use a private default SQLite audit sink, required the
complete four-report set and coherent v3 checkpoint evidence, and moved
serialized validator admission before source access. The strengthened exact
candidate-process gate passed in 125.79 seconds. The full canonical suite and
release-source process tests also pass. No release, installation, connector
refresh, or authenticated call has run. PDF activation still requires the
authorized release boundary and fresh
authenticated journeys for a small text/visual PDF, a scanned visual/OCR PDF,
and the exact near-ceiling PDF. Other official document families remain
unsupported and keep Issue #4 open.
