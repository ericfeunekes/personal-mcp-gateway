---
title: "Testing"
status: draft
purpose: "Define proof expectations for the local MCP gateway."
covers:
  - cmd/
  - internal/
  - docs/requirements/
---

# Testing

Proof must match the claim. This repo handles personal data, so green unit tests are not enough for claims about vault safety, tunnel behavior, or low machine impact.

## Proof Surfaces

| Risk or behavior | Primary proof surface | Required when |
| --- | --- | --- |
| MCP tool registration and schemas | Boundary tests around `internal/mcp` and server/tool registration | Adding or changing tools |
| Vault path confinement | Filesystem adapter tests with temp fixture vaults, traversal cases, symlink escapes, and denied file patterns | Any filesystem behavior changes |
| Tool authority and vault non-target isolation | Integration snapshots proving the original five tools and mutation-scoped `stat` remain read-only, while each mutation changes only its explicit allowed target | Any Obsidian tool change |
| Mutation preconditions and atomicity | Vault-boundary race tests for opaque source fingerprints, explicit absence, collision-safe no-replace, whole-patch validation, prior-or-final visibility, and no automatic replay of uncertain outcomes | Adding or changing mutation behavior |
| Mutation interruption and cleanup | Production-boundary subprocess checkpoints for cancellation, timeout, injected I/O failure, hostile external replacement, SIGKILL, temporary-file cleanup, descriptor recovery, and no shadow/undo state | Adding or changing mutation behavior |
| Search and listing limits | Large fixture tests with timeout, depth, byte, result, cancellation, oversized-literal streaming, bounded-evidence, and regex-line-cap assertions | Any traversal or search change |
| Structured telemetry | SQLite and JSONL proof matrix covering event families, sanitized identifiers, sink degradation, and no raw path leaks | Any audit or tool-call behavior change |
| Local release transaction lifecycle | Executable state/event matrix plus process tests for locking, crash-boundary reconciliation, exact-hash accept/rollback, first-install unload, recovery-artifact retention, and installed-service pending-to-terminal journeys | Any change to release, update, rollback, acceptance, or supervised-runtime activation behavior |
| Obsidian server tool names in ChatGPT | Live smoke test through OpenAI Secure MCP Tunnel | Before treating connector compatibility as settled |
| Minimal machine impact | Local process observation for idle CPU, memory, file descriptors, startup behavior, and no whole-vault startup scan | Before always-on usage |
| Native document transfer | Exact repeated stdio and HTTP calls through the candidate serializer, original-byte/MIME identity, malformed/spoof/source-change negatives, response deadlines, one-shot cleanup, and authenticated tunnel/model journeys | Adding a format or changing capture, validation, handoff, or transport |

## Expected Commands

### YNAB endpoint

Run `make test-ynab` for the quota-free change-validation profile. It strips
provider/tunnel/model credentials and runs the YNAB, MCP, app, config, gateway
entrypoint, and schema-generator suites. It is a subset of `make test`, not a
replacement for the full repository gate. Tests use synthetic tokens and
temporary state; no 1Password lookup or live YNAB call is needed. Go may need
its normal dependency download on an uncached machine; that is not provider QA.

`recovery_boundary_test.go` owns the model-visible recovery journey through SDK
dispatch and a scripted local HTTP server. The simulator's transport rejects
non-loopback addresses, redirects, and environment proxies. Controlled failures
cover 429, credentials, transient read retry, uncertain writes, malformed output,
quota consumed by retries, partial results, and a single real HTTP recovery probe.
`recovery_process_test.go` runs separate MCP stdio processes against real local
SQLite and HTTP: clients compete for the last quota slots, restart through a
cooldown, isolate different tokens, and retain an in-flight reservation after a
process is killed. A test clock advances the rolling window without waiting an
hour. `quota_test.go` owns the pure circuit transition table and store boundaries,
including stale completions, lease expiry, shared cooldowns, private files, and
unavailable state. Production API URLs and recovery timing remain non-configurable;
the injected endpoint and clock exist only in package-private test construction.

The local simulator proves the adapter's HTTP and recovery semantics, not YNAB's
availability or undocumented headers. The frozen public OpenAPI fixture anchors
resource shapes; the official rate contract is 200 requests per rolling hour.
Live revalidation, when separately requested, remains distinct from these tests.

Recovery follow-up proof, 2026-09-07: `make test-ynab`, the full YNAB race suite,
and the final frozen-tree `make test` all passed. Negative mutations caught an
incorrect 201-request allowance, per-process rather than shared databases, and
Go transport reuse that sent three HTTP requests for two reservations. The
normal transport matched actual HTTP requests to reservations. Canceled probes
and stale completions are covered at model/store and real HTTP boundaries.
The final production schema render retained all twelve declarations without
`unknown` (largest input 5,309 bytes; largest output 7,451 bytes). Only `create`
advertises export artifacts; a regression test protects this truthful restriction.
No live YNAB quota was consumed, and no runtime service was activated by this proof.

Local implementation proof on 2026-09-07: `make test` passed, including the
sequential gateway-smoke and release-script stages. Race checks passed for
YNAB, MCP, app, config, release activation, and both gateway/release entrypoints.
The candidate build, affected documentation graph, and `git diff --check` passed.
These results cover the working-tree implementation, not an installed release.

The canonical suite includes the frozen OpenAPI v1.86.0 operation inventory
(all 44 method/path operations), generated-schema comparison, real local HTTP
provider fixtures, SDK stdio/HTTP dispatch, and gateway subprocess selection.
Run the focused boundary suite with `go test ./internal/tools/ynab ./internal/mcp
./internal/app ./internal/config ./cmd/gateway ./cmd/ynab-schema-gen`.
`go run ./cmd/ynab-schema-gen` checks the published provider schema;
`go run ./cmd/ynab-schema-gen -write` explicitly regenerates it from the checked-in
public specification. No network or provider credentials are needed for generation.

Fixtures prove ordered batching, stable ID correlation, read-error continuation,
429 stop/Retry-After, uncertain writes after dispatch, cancellation, and two-call
provider concurrency. Filesystem tests prove private exports, complete JSON,
CSV units/escaping/splits, exclusive publication, ancestor symlink rejection,
serialized exports, canceled-write cleanup, and the actual 32 MiB provider and
64 MiB rendered-export ceilings. Raw HTTP calls preserve integer milliunits above
2^53 in both directions. Actual MCP calls through JSONL and SQLite retain counters
but no financial data, token, or export path. Release tests cover both captured
services, crashed loaded jobs, configuration drift, first-install cleanup,
forward activation, and rollback through the shared authority.
Mixed-batch MCP calls also prove malformed targets, dates, and splits are rejected
before any provider request. Adjacent near-limit fixtures cross actual HTTP and
stdio framing: complete success frames measured 188,554/188,555 bytes; the next
larger fixture returned compact errors without records or change tokens. This
guards the public 192 KiB ceiling independently of the internal size estimator.

On 2026-09-07 the built production YNAB server's twelve input/output declarations
rendered without `unknown` using the inspected Codex renderer. The largest input
was 5,309 bytes and largest output 7,769 bytes. A fresh metadata-only Codex run
recognized all six tools, required transaction dates, scope alternatives, nullable
updates, host-export fields, and typed output collections; it constructed list and
export examples without invoking them. This is model-visible schema evidence,
not authenticated YNAB execution or ChatGPT compatibility.

The SDK 1.7.0 production-candidate native PDF capacity gates passed over stdio
and HTTP, preserving the separate Obsidian boundary. Authenticated YNAB reads,
disposable live write targets, installed YNAB idle-impact checks, and actual
two-service tunnel activation remain release proof. ChatGPT model invocation and
native JSON/CSV downloads are explicitly deferred.

### Full repository

Canonical test command:

```bash
make test
```

The target discovers every Go package, runs ordinary packages together so Go
can retain package-level concurrency, and then runs `./internal/tools/ynab`,
`./cmd/gateway-smoke`, and `./scripts` as separate sequential stages. Every stage uses `go test -count=1`,
so the release gate executes the process and boundary tests rather than reusing
prior successful results. The isolated stages prevent the performance/resource
smoke and release watchdog suites from competing with heavyweight packages or
with each other while their production thresholds remain unchanged.
The YNAB stage also isolates its multi-process SQLite quota/crash tests from the
filesystem deadline tests. Both run unchanged; isolation prevents their real
disk and process workloads from distorting each other's timing.

When running inside a restricted agent sandbox that cannot write the default Go
build cache, keep the build cache repo-local:

```bash
GOCACHE=$(pwd)/.gocache make test
```

For shallow-list enumeration changes, run the fixed 1,000-entry, ten-result
filesystem and non-root tool-boundary benchmarks with allocation reporting.
Compare the same command and machine conditions before and after the candidate;
keep a hill-climb only when allocations improve and median latency does not
regress by more than five percent:

```bash
GOCACHE=$(pwd)/.gocache go test -run '^$' -bench '^Benchmark(ListPage|LS)LargeDirectory' -benchmem -count=5 ./internal/fsx ./internal/tools/obsidian
```

The canonical suite includes the existing gateway proof below. The pending
release implementation must add the stated lifecycle coverage before it is
treated as accepted:

- config validation and loopback bind rejection tests;
- root-confined filesystem adapter tests for traversal, absolute paths, hidden entries, symlink traversal, limits, cancellation, and read-only behavior;
- mutation adapter tests proving `stat` fingerprints allowed regular files and
  empty directories without exposing filesystem identity, and rejects a
  non-empty directory after examining at most one entry; create and destination
  absence cannot collide; stale source fingerprints cannot replace, patch,
  move, or delete a newer source; multi-replacement patches validate every
  unique non-overlapping match before effect; and rejected calls preserve both
  the target and every non-target path;
- process-boundary mutation tests at the production commit points proving
  collision-safe no-replace behavior under hostile external name replacement,
  complete prior-or-final visibility under cancellation, timeout, injected I/O
  failure, and SIGKILL of the gateway and commit helper separately—including
  stage creation before identity announcement—symmetric chosen-name/identity
  cleanup, no content-bearing temporary residue after
  healthy-scheduler quiescence, exact descriptor cleanup,
  permanent-delete truthfulness, and no automatic retry after an
  uncertain post-commit result;
- grep boundary tests proving literal mode searches complete oversized physical
  lines while returning explicit bounded UTF-8 evidence, continues to account
  for byte budgets and invalid UTF-8 beyond the excerpt, and leaves regex mode's
  1 MiB physical-line rejection intact;
- SDK subprocess stdio tests through `cmd/gateway`;
- process-level tests proving both production stdio/tunnel wrappers fail fast
  and do not print configured host paths when the vault root is unavailable;
- process-level release tests proving clean-tree preflight, ordered test/build
  execution, exact candidate installation, secret-safe output, rollback after
  failed readiness, retained recovery artifacts when rollback is unconfirmed,
  source/candidate mutation rejection, candidate/install path separation, and
  main-only exact-remote updates. Pending-release changes require the full
  state/event matrix, fail-fast contention, lock-held update races,
  interrupted-transition fixtures, exact installed/artifact hashes, stale-ID
  rejection, pinned-controller selection, first-install unload, and retained
  recovery evidence. Dispatcher/process cases separately assert stdout, stderr,
  and exit status; cover dangling `active`, child-start failure, clear-to-active
  and authority-A-to-B races, one retry only, output above 64 KiB, exact active
  guidance, and suppression of hostile gate/child diagnostics;
- a loopback boundary test proving live verification checks both tunnel
  `/healthz` and `/readyz` after confirming the LaunchAgent is loaded from this
  checkout's tunnel wrapper;
- SDK Streamable HTTP tests through `/mcp`;
- `/healthz` and `/readyz` tests, including fail-closed readiness when the root disappears.
- structured telemetry tests for SQLite persistence, JSONL output, tool-call success and errors, HTTP request events, MCP request events, gateway lifecycle events, sink degradation, and subprocess stdio events written to a temp SQLite database.

## Structured Telemetry Proof Matrix

Local server-side telemetry proof must cover both JSONL and SQLite where
applicable. A valid proof pass includes:

- `tool.call` success, `path_denied`, `schema_validation`, `unknown_tool`, and
  `limit_exceeded` rows;
- decoded SQLite `body_json` assertions, not only row counts;
- indexed SQLite `method`, `tool`, `outcome`, and `error_code` assertions;
- hostile caller-controlled tool names and argument keys proving unknown values
  are bucketed or run-hashed rather than stored raw;
- `mcp.request` rows for non-tool MCP requests such as `tools/list`;
- `http.request` rows for `/healthz`, `/readyz`, and `/mcp`, including an
  oversized `/mcp` body rejection for known-length and unknown-length request
  bodies;
- HTTP SDK calls with oversized tool arguments that remain under the HTTP body
  cap and produce bounded telemetry summaries;
- `gateway.start`, `gateway.backend_ready`, `gateway.stop`, and at least one
  subprocess `tool.call` row from the CLI path;
- `gateway.backend_ready` tool names compared with SDK `ListTools`;
- the repo-owned stdio message limit using a raw oversized subprocess frame;
- post-start telemetry sink degradation using fake sink and real temp-SQLite
  failure paths, plus CLI close-failure stderr/exit proof;
- no raw vault root, host path, note path, destination name, note or attachment
  content, mutation fingerprint, tunnel credential, host identity, or token
  material in JSONL text, SQLite indexed columns, or SQLite `body_json`.

Local SDK and HTTP tests prove only the server side. They do not prove
model-driven Codex behavior or ChatGPT connector behavior through OpenAI Secure
MCP Tunnel.
They also do not prove telemetry for SDK-unsupported MCP protocol methods,
which the SDK rejects before gateway middleware observes a request.

If linting or race tests are added, document the exact commands here before treating them as required gates.

For bounded-concurrent `grep` changes, run the affected race and exact-candidate
proofs in addition to `make test`:

```bash
GOCACHE=$(pwd)/.gocache go test -race -count=1 ./internal/tools/obsidian ./internal/app
GOCACHE=$(pwd)/.gocache go test -count=1 ./cmd/gateway-smoke -run '^TestPhase2PerformanceExactCandidate$' -v
GOCACHE=$(pwd)/.gocache go test -count=1 ./cmd/gateway-smoke -run '^TestPhase2ResourceProbeExercisesBuiltTenToolCandidate$' -v
```

The resource proof must observe overlapping request-local pools above one
eight-worker ceiling, bounded active and reserved work, cancellation isolation,
immediate FD/vault quiescence, and a successful same-session follow-up.

## Retained Resource Gate

Resource report schema v8 preserves the unchanged 256 KiB retained-heap, 8 MiB
retained-RSS, and 64 MiB lifetime high-water limits. The three post-GC batch
heap deltas are measured from the aligned baseline, and the retained value is
their second-largest nonnegative delta; a heap breach must therefore persist in
at least two batches. For RSS, each baseline and batch stabilization window is
the maximum of its immediate, five-second, and 30-second samples, and the gate
uses the second-largest nonnegative baseline-window-to-batch-window delta. The
raw checkpoints, blocking-GC acknowledgements, FD recovery, idle, workload,
latency, boundary, and lifetime high-water gates remain independently required.
Every report identifies its exact `tool_surface`: canonical release proof uses
the eleven-tool `candidate` surface. The ten-tool default non-document surface
is available only through explicit `--resource-control` with `--resource-json`;
it exists for paired regression comparison and is rejected by canonical
report-set validation.

The PDF activation candidate additionally runs an exact 7,000,000-byte gate.
It generates and independently validates/renders a two-page PDF, makes three
sequential native calls in one stdio session and three in one HTTP session,
checks original SHA-256/MIME/terminal evidence, and launches the built candidate
for both transports. The HTTP half additionally proves that one of two competing
near-ceiling calls fails with sanitized `document_busy`, retry succeeds, an
actual 7,000,001-byte `.pdf` and four other unsafe inputs return their exact
structured errors without native content, and the vault is unchanged. A real
stalled client must produce no server failure before 29 seconds, then hit the
30-second response deadline and emit a sanitized operational write-failure
signal before 34 seconds while the client body remains stalled. Only then does
the harness release the client, verify client failure and payload/admission
recovery, and permit a fresh SDK session to the same candidate process to retry
successfully. (The timed-out response is deliberately truncated, so its
original JSON-RPC client session is not reusable.) Each transport retains the
64 MiB lifetime high-water and
8 MiB retained-RSS limits. Stdio additionally proves blocking-GC cleanup,
quiescence, exact FD recovery, same-session follow-up, and a 60-second idle
window; HTTP proves same-session follow-up, no FD increase, and retained RSS
after 30 seconds. The report separately measures the bytes-only validator
helper and requires the larger gateway high-water plus helper high-water to fit
a conservative 160 MiB aggregate upper bound. The heavyweight local check is
opt-in outside release. Its HTTP candidate uses a private default SQLite audit
sink so the proof includes the production `statusRecorder` unwrap, telemetry
cost. Schema v4 requires 14 persisted tool-call rows, decoded safe summaries,
indexed-field/body parity, the exact success/error distribution, and absence of
fixture names, paths, nonce, native URI, and content sentinels. The fixture-vault
before/after digest recursively binds paths, modes, sizes, modification times,
regular-file bytes, and symlink targets:

```bash
make build
ISSUE4_EXACT_DOCUMENT_GATE=1 GOCACHE=$(pwd)/.gocache \
  go test -count=1 ./cmd/gateway-smoke \
  -run '^TestExactNativeDocumentCapacityCandidate$' -v
```

`make release` runs the same document-capacity report unconditionally against
the exact candidate SHA before installation. Its fixture is written only in an
isolated proof-vault subdirectory of the private release-report directory, so
candidate/report artifacts cannot enter the vault snapshot; validator backing
is unlinked before any document bytes are written.

## Mutation Phase Proof Contract

This section is the acceptance bar for mutation implementation. The current
macOS API has no existing-name full-source-version compare-and-change namespace
primitive. The approved contract therefore requires exact fingerprint,
absence, and patch-context validation at the last available pre-effect
checkpoint and accepts the narrow external-writer window between that checkpoint
and the namespace syscall. Proof must demonstrate every observable pre-effect
mismatch and must not relabel this boundary as full CAS. A final-state-only
check or advisory lock cannot satisfy these cells.

Issue #3 adds an exact five-tool mutation delta to the accepted server:
read-only mutation-scoped `stat`, plus `write`, `edit`, `move`, and `delete`.
The current accepted baseline is the five read-only tools `grep`, `ls`, `read`,
`read_many`, and `resolve`, but independently accepted graph tools may expand
that baseline before mutation is unblocked. The then-current functional,
performance, resource, release-fake, and report-set contracts remain evidence
for that accepted baseline only. They must be deliberately versioned or
replaced before a mutation candidate can pass; adding handler unit tests while
retaining a release grammar that omits the five-tool delta is failed proof.

### Issue #6 foundation gate

Before any issue #7 public-surface implementation begins, private
production-boundary `fsx` and gateway-helper subprocess proof must pass for:
vault confinement and same-directory staging; file source fingerprints; empty
directory fingerprints with at-most-one-entry emptiness checks, nanosecond
source stamps, and add/remove membership races; stale fingerprint and
absence rejection; `RENAME_EXCL` collision behavior and unsupported-volume
failure; helper protocol/version, 4 KiB frame, two-second deadline, four-helper,
four-stage, 8 MiB-per-stage, mode/kind/link/owner/descriptor bounds; helper-owned final
revalidation and commit; parent/helper SIGKILL separately including the
stage-created-before-identity-announcement checkpoint; private-stage cleanup's
last-unlink race and targeted-writer exclusion; simultaneous
or sequential owner loss named as excluded; prior-or-final public visibility; uncertain
post-commit no-replay; accepted final-syscall race documentation; and FD,
process, stage, and vault-activity quiescence. This gate adds no MCP schemas,
descriptors, telemetry, candidate reports, connector metadata, deployment, or
personal-vault mutation. Those are issue #7 proof surfaces.

The mutation merge and release candidate must prove all of the following:

- `tools/list`, backend-ready telemetry, descriptor grammar, and annotations
  agree on the then-current accepted baseline plus exactly the five mutation
  tools. Every previously accepted tool and `stat` retains its declared
  read-only/destructive truth; `write`, `edit`, `move`, and `delete` are not
  read-only and are destructive, including `move` because it removes the source
  path. Every mutation descriptor explicitly sets `openWorldHint:false` and an
  intentional `idempotentHint:true` consistent with exact one-effect
  preconditions and no automatic uncertain-outcome replay.
- SDK stdio and Streamable HTTP calls cover `stat` for a regular file and empty
  directory plus create, complete replacement, structured multi-replacement
  patch, move, and permanent delete. Follow-on `stat` and `resolve` observe the
  canonical committed state.
- The public SDK stdio and Streamable HTTP journeys retain representative
  adapter proof for stale fingerprints, create collision, missing patch
  context, and denied traversal, with a follow-on fingerprint proving no
  effect. The deterministic adapter tests own ambiguous and overlapping
  matches, mixed/non-canonical encodings, aggregate patch limits, and result
  limits. The issue #6 `fsx` and helper suites remain the authoritative
  production-boundary injection proof for destination collision, denied kinds,
  non-empty directory deletion, cancellation, timeout, I/O failure,
  same-inode in-place source changes, directory-membership changes, and hostile
  name races before final revalidation. Those lower-boundary cases are not
  redundantly replayed through every MCP transport; public schema, descriptor,
  error-translation, and telemetry tests bind the adapter to that foundation.
  Every refusal cell proves unchanged target and non-target fixture state. A
  separate foundation checkpoint demonstrates and documents the accepted
  unprotected interval after helper-owned final revalidation without asserting
  a no-effect rejection.
- Exact-candidate functional reports retain operation outcomes and bounded safe
  counts without path, destination, content, patch value, fingerprint, cursor,
  or host identity. Performance and resource reports retain the accepted
  read-only workloads and add repeated representative mutation cycles that
  recover temporary files and descriptors exactly, remain within the existing
  heap/RSS/FD/CPU thresholds, retain no mutation authority or content-bearing
  residue, and show no continuing
  resource or vault-activity growth after quiescence.
- Representative successful and refused mutations are persisted through the
  real SQLite sink, then both indexed columns and independently decoded
  `body_json` are checked for raw source paths, destination paths, content,
  patch values, and fingerprints. The built stdio functional journey performs
  the equivalent JSONL non-disclosure check.
- Release fake reports and cross-report validation use the same accepted
  baseline plus five-tool mutation delta. A report that omits the mutation
  delta cannot satisfy the mutation candidate gate.

Platform-specific atomicity proof must name the actual macOS commit primitives
and checkpoints used by the implementation. A helper-only test is insufficient:
proof must exercise external replacement immediately before helper-owned final revalidation,
the explicitly accepted window after revalidation, and subprocess interruption
of the parent and helper separately before and after the commit token. A
post-commit failure may report an uncertain outcome,
but it must never expose partial public bytes, silently overwrite an
absent-destination collision, leave content-bearing staging residue, or trigger
an automatic replay. Sequential loss of both owners, process-group loss, OS
crash, power loss, and a writer deliberately targeting the private stage name
are named exclusions rather than implied proof.

## Test Data Rules

- Use generated fixture vaults in tests.
- Do not commit real vault files, exported personal data, secrets, or tunnel credentials.
- Keep fixture note content synthetic and small unless a specific performance fixture is needed.

## Live-Service Proof

Use live OpenAI tunnel/ChatGPT verification only for behavior that cannot be proven locally, such as connector compatibility for the `obsidian` server and its tool names. Record the date, server name, tool names tested, and observed result in closeout notes or the relevant requirement doc.

For a local deployment change, `make release` is the canonical source-to-runtime
proof. It includes the full suite, an MCP stdio `resolve(.)` probe against the
exact candidate, byte-for-byte installed binary verification, LaunchAgent
restart, and bounded tunnel liveness/readiness checks. Passing those local gates
must leave the transaction `pending` and rollback-capable; it does not prove that
ChatGPT or another remote model selected and completed a tool call.

The release proof contract is split into two current-state cells. A release
does not have to prove rollback before acceptance: the automated lifecycle
suites in cell 1 cover rollback, and `make release-rollback` remains available
while a release is pending.

1. Run the canonical merge proof, including the executable lifecycle matrix and
   process/crash/concurrency coverage:

   ```bash
   GOCACHE=$(pwd)/.gocache go test -count=1 ./internal/releaseactivation ./cmd/release-activation ./scripts
   GOCACHE=$(pwd)/.gocache go test -race -count=1 ./internal/releaseactivation ./cmd/release-activation ./scripts
   make test
   git diff --check
   ```

2. Select QA from every changed boundary. Before release, state in one sentence
   what changed and which applicable rows below can falsify it. Applicable rows
   are cumulative for mixed changes. A surface-specific row such as mutation or
   native documents cannot be downgraded to the generic metadata row, although
   metadata refresh still applies when its descriptor changed. Run no broader
   journey merely because the server also exposes other capabilities.

| Changed boundary | Required change-scoped proof |
| --- | --- |
| Tool names, schemas, descriptions, or annotations | Refresh server `obsidian` metadata, inspect the affected descriptors, and make a representative affected call when the wording or schema governs tool selection or arguments. |
| Retrieval paths, output coordinates, batching, search, or cursors | Run one user-like read-only journey through the affected operations. Continue a cursor only when continuation changed. Prefer one coherent task over one call per tool. |
| Mutation schemas, preconditions, fingerprints, result paths, or filesystem effects | Run the approved disposable mutation journey below and read back its sanitized telemetry and vault postconditions. |
| Native-document capture, validation, MIME, admission limit, serializer, transport, or document-tool schema | Run the three PDF journeys below plus the local byte/MIME and capacity gates. Metadata-only wording changes need only metadata refresh and a representative call. |
| Tunnel, authentication, MCP transport, or client handoff | Run a representative affected call through the authenticated tunnel and correlate sanitized dispatch evidence. |
| Telemetry summaries or privacy | Exercise the affected tool/outcome and inspect the configured sink for the changed bounded fields and prohibited data. |
| Release controller, installed replacement, supervision, or recovery | Run cell 1 and `make verify-live` for each loaded service; add an authenticated tool call only when the connector boundary also changed. |
| Internal performance or allocation work with unchanged public behavior | Use the candidate-bound local performance/resource gates and one representative affected tool call. Do not replay unrelated destructive or native-document activation journeys. |
| Documentation only | No binary release or authenticated model journey is required. Verify the documented commands and affected documentation neighborhood. |

Examples: a retrieval-plus-telemetry change selects both rows and may satisfy
them with one coherent retrieval journey plus sink readback. A lifecycle-only
change selects the lifecycle row. A documentation-only change does not create a
release transaction. After rollback of a lifecycle-only candidate, the restored
hash and ready runtime close the tested boundary without waiting for ChatGPT.

Exact tool-set and full activation journeys remain durable proof for the release
that introduced or changed those surfaces. They are not universal prerequisites
for every later release. Only the applicable current-change rows gate exact-ID
acceptance. An unrelated ChatGPT/model issue discovered during exploratory QA is
recorded as a separate finding; it does not block acceptance only when evidence
shows it neither prevents nor contradicts any selected boundary.

When the current change selects the mutation row, explicit approval to begin
the live personal-vault journey is required. A fresh model
run must perform `stat` on an allowed disposable file or directory as needed,
then `write(create)` -> `stat` -> structured multi-replacement `edit` -> `move`
on a single disposable allowed target. Follow-on `stat` and `resolve` must
confirm the moved state, then the operator pauses and obtains a second fresh
explicit approval immediately before the permanent `delete` call. Approval to
begin the journey does not pre-authorize that final call. Sanitized readback
from the installed runtime's configured telemetry sink must show the
corresponding operation kinds and outcomes without retaining the target,
destination, content, patch values, or fingerprints. The current LaunchAgent
uses SQLite; the local candidate proof matrix separately covers equivalent
JSONL output. Only after that journey may exact-ID acceptance return the release
to `clear`. Do not treat every unsuccessful ChatGPT turn as a candidate
failure. First determine whether the requested tool call reached the installed
gateway by correlating the authenticated turn with sanitized telemetry and the
expected vault state:

- Roll back when evidence implicates the candidate or installed runtime. This
  includes an advertised tool or annotation mismatch, a dispatched call that
  returns an unexpected error or result, a missing or unsafe telemetry record
  for a dispatched call, an incorrect vault postcondition, an installed-hash
  mismatch, or failed health/readiness.
- Do not roll back for a failure outside the candidate that is proven to occur
  before dispatch, such as a ChatGPT policy refusal, authentication failure,
  usage limit, or client/UI outage. Keep the release `pending`, do not accept
  it, record the no-dispatch evidence, and retry or resolve the external gate.
- If dispatch cannot be established, inspect both sanitized telemetry and the
  exact allowed target before acting. Keep the release pending while the cause
  is resolved; roll back only if candidate/runtime failure is found or Eric
  chooses to abandon the pending attempt.

After a candidate/runtime failure, exact rollback must restore the prior hash,
ready runtime, and relevant postconditions. When the connector contract changed
or the failure crossed the authenticated connector boundary, also refresh prior
metadata and make one successful prior-contract call. A lifecycle-only rollback
does not wait on unrelated ChatGPT availability. Local SDK success alone never
authorizes acceptance of a connector-affecting candidate.

The installed-service step above is a real mutation of Eric's personal vault.
Even though the target must be newly created, disposable, and allowed, the
operator must obtain Eric's explicit authenticated approval before its first
effect and pause again for fresh approval immediately before permanent delete.
If the second approval is withheld, leave the moved disposable target unchanged
and report that cleanup still requires authorization. Issue scope and a passing
local candidate authorize implementation and preparation; they do not authorize
either live-vault effect or cleanup.

When the current change selects the native-document row, use only generated
synthetic fixtures whose staging, use, and removal Eric has explicitly
authorized at the live vault boundary; those are separate vault effects and are
not implied by release authorization. In fresh
authenticated ChatGPT runs, require model-selected `read_document` for a small text/visual PDF, a
scanned visual/OCR PDF, and an exact 7,000,000-byte PDF. Generate expected
text and visual challenge answers outside the model prompt and conversation;
the prompt asks for the evidence but never supplies the expected values. The
exact-ceiling fixture must place distinct answer-bearing evidence on its first
and final logical pages, with the final evidence sourced from an object near the
tail of the bound file; the model must report both correctly. Each run must use
the connector-returned artifact through ChatGPT's one-time native-materialization
consent without a manual download, upload, or reattachment. The operator
compares the returned interpretation with the withheld fixture answers.
Sanitized readback from the installed runtime's configured telemetry sink must
show PDF format, outcome, latency, and bounded byte counts without retaining the
path, name, bytes, extracted content, or opaque identity. The current
LaunchAgent uses SQLite; equivalent JSONL privacy is proven by the local
candidate matrix. The local original-byte/MIME and capacity reports remain
required but do not substitute for this authenticated client proof. Retain only
the boolean challenge comparisons and sanitized journey outcome; discard the
challenge values after evaluation.

Every applicable row must succeed; unaffected rows are not acceptance gates.
Apply the dispatch-based classification above: roll back for candidate or
installed-runtime failure, but keep the candidate pending for an external
failure that prevents selected proof. A model interpretation mismatch after
successful native delivery blocks when native interpretation is selected or
when it contradicts another selected client-visible boundary. Otherwise it is
a separate client QA finding, not a reason to hold the release transaction
open.

Record sanitized release identity and hash prefixes, the authenticated surface,
metadata observation, selected tool/journey, and terminal outcome. Do not record
prompts, note names or content, vault paths, credentials, raw environment data,
or private manifest fields. The installed drills establish a representative
activation transaction, not power-loss durability, sleep/wake recovery,
multi-day soak behavior, every prompt formulation, or future-vault performance.

### Current local eleven-tool combined-candidate proof

On 2026-07-26, the rebased candidate combines the five-tool retrieval baseline,
`stat`, `write`, structured `edit`, `move`, permanent `delete`, and the
activation-gated `read_document` PDF candidate. The default non-document
surface therefore has ten tools and the activation candidate has eleven. The
ordinary package set includes the Darwin
mutation-foundation suite; the built-gateway smoke suite passed in 34 seconds;
and the release-script suite passed in 215 seconds.

The exact-candidate functional v5 report runs a disposable create, stat,
complete replacement, multi-replacement edit, move, readback, permanent delete,
and final resolve journey through both stdio and Streamable HTTP on the built
gateway executable. It also proves collision, stale-fingerprint, missing-patch-
context, overlapping and ambiguous patch context, exact and one-byte-over
512 KiB write limits, exact and one-byte-over 8 MiB edit limits, and denied-path
refusals through both transports. Every refusal is followed immediately by a
complete vault snapshot; traversal also verifies an explicit outside-root
sentinel. Performance v7 and resource v9 each run three additional 14-call
disposable mutation journeys, require 42 journey calls (27 mutation-tool calls
and 15 read-only `stat`/`read`/`resolve` observations), compare complete fixture name,
kind, size, and content-digest snapshots for residue/non-target isolation,
verify exact descriptor and telemetry quiescence, and retain only bounded counts
and boolean outcomes. Cross-report
and release-fake validation require the same eleven-tool candidate grammar and
reject the prior report schemas. The current-vault performance and cold-start
probes remain read-only.

This is local connector-facing proof against generated disposable vaults. It is
not an installed-service mutation, authenticated ChatGPT discovery, deployment,
release acceptance, or personal-vault destructive journey. Those steps remain
separately authorized release work under the mutation cell above.

### Historical accepted five-tool Phase 2 proof

On 2026-07-17, implementation commit `d74fcd3ba1b1`, installed candidate hash
prefix `2de6c5f23082`, and release prefix `725425303f84` passed `make test`, the
exact-candidate functional v3, performance v3, historical resource v5, and cross-report
gates, and the installed pending-release readiness checks. Authenticated Chrome
Refresh then showed exactly `grep`, `ls`, `read`, `read_many`, and `resolve`, all
read-only.

A fresh ChatGPT run made exactly three calls: one `grep`, one first-page
`read_many`, and one continued `read_many`. Sanitized telemetry recorded 20
matches across six files before the discovery result limit. The first batch and
continuation each supplied the same three ordered requests and a 300-byte
aggregate budget; the first omitted a cursor and the second supplied one. Both
batch calls completed successfully with stable consistency, and the continuation
validated one prior source entry. Successful continuation under the unchanged
request vector and budget proves cursor-bound query/budget reuse because either
change is rejected as `cursor_mismatch`; the visible result also confirmed
advancement without replay.

Exact-ID acceptance returned the transaction to `clear`; the exact candidate
hash remained installed, and the LaunchAgent and tunnel were live and ready.
The current-vault report covered 7,257 Markdown files totaling approximately
292 MiB. Current-vault p95 latency ranged from approximately 1.3 to 3.5 ms;
synthetic `read` and `grep` p95 were 7.4 ms and 72.1 ms, and measured 10,000-file
strata remained below 44 ms. Historical Resource v5 recorded 8,884,224 bytes
maximum high-water RSS growth, 2,371,584 bytes stabilized 30-second RSS growth,
185,240 bytes heap growth, exact descriptor recovery after 312 calls, and zero CPU,
tool-call, or vault-activity growth during the 60-second idle window. No prompt,
pattern, note identity, path, content, cursor value, or cursor hash is retained
in this record.

### Historical release-activation proof

On 2026-07-12, commit `82e036e8a778` completed the full normal/race merge proof
and the real randomized first-install LaunchAgent rollback drill. The installed
rollback journey reached pending release prefix `879533cf5e89`, rejected missing
and wrong IDs without changing that transaction, restored previous hash prefix
`4c2ca327428f`, returned to `clear`, and was ready. A separate installed journey
reached pending release prefix `0c58c7303cdf` with candidate hash prefix
`8f06b65b2bf7`; authenticated metadata exposed exactly direct server tools `ls`
and `resolve`, and model-selected `ls(path=".", limit=1)` succeeded with a bounded
one-entry truncated response. Exact-ID acceptance returned to `clear`; the
installed hash still matched `8f06b65b2bf7` and the service was live and ready.
No note names or content were retained in this proof record.

On 2026-07-15, implementation commit `fa9e02983936` first passed the forward
Phase 1 activation on candidate hash prefix `232441cc6a34`. Three earlier failed
model journeys had been exact rolled back with previous-hash and readiness
proof, but had missed the required authenticated prior-schema refresh and old
call. A controlled replacement drill therefore accepted prior-contract commit
`9612507308a6` / hash prefix `8f06b65b2bf7`, published current release prefix
`9fa5316d0384`, refreshed and observed the candidate-only cursor schema, then
exact rolled it back. Rollback restored `8f06b65b2bf7`, `clear`, live, and
ready; authenticated Refresh removed `cursor`, restored the prior descriptions,
and a brand-new model chat completed one successful old-contract `ls` call.

The final Phase 1 release used docs descendant `241df06d5ede`, release prefix
`557d434272a3`, and the same implementation hash prefix `232441cc6a34`.
Authenticated Refresh issued a post-install `tools/list`; fresh management
readback showed exactly `ls` and `resolve`, including the current `ls.cursor`
schema and continuation guidance. A model-selected journey made exactly two
`ls(limit=1)` calls in one run. The first omitted `cursor`; the second supplied
a 248-byte cursor with the same root path correlation, absent `base`, and
unchanged limit. Both calls completed below the one-millisecond telemetry
resolution, returned one entry with cursor continuation, and measured 700/588
and 704/592 bytes for total SDK/structured results. Joined telemetry plus an
immediate same-binary parity probe retained
`cursor_reused_from_prior_result:true`,
`normalized_query_unchanged:true`, and page advancement without retaining the
cursor or entry identities. The visible answer reported two inspected items
and that more remained; no other tool call or error was recorded. Exact-ID
acceptance returned to `clear`; `232441cc6a34` remained installed, and the
LaunchAgent and tunnel were live and ready. No prompt, note identity, cursor
value/hash, vault path, or content is retained in this record.

### Historical accepted `resolve` / `ls` Phase 1 proof

On 2026-07-15, implementation commit `fa9e02983936`, final release descendant
`241df06d5ede`, and installed candidate hash prefix `232441cc6a34` passed
`make test`, the exact-candidate release gates, the full documented normal/race
release-lifecycle suites, and focused affected normal/race suites for
`internal/fsx`, `internal/tools/obsidian`, `internal/mcp`, `internal/audit`,
`internal/app`, and `cmd/gateway-smoke`. The same implementation passed the
opt-in randomized first-install LaunchAgent rollback drill after the advertised
schema changed.

The exact-candidate smoke observed exactly two tools, canonical stored-path
resolution, three one-entry synthetic pages with second-page progress and no
duplicates, complete reference equivalence, and a maximum observed SDK result
of 718 bytes. The sanitized 100-sample current-vault cached performance gate
used a `2_10` cardinality bucket and recorded:

| Operation | p50 | p95 | max | max SDK bytes | max structured bytes | max entries scanned |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| cached `resolve` | 603 us | 2,471 us | 4,867 us | 213 | 101 | 0 |
| first-page `ls(limit=1)` | 762 us | 1,991 us | 3,899 us | 896 | 784 | 2 |
| continued `ls(limit=1)` | 552 us | 1,998 us | 4,241 us | 515 | 403 | 2 |
| first-page `ls(limit=100)` | 621 us | 2,103 us | 3,786 us | 689 | 577 | 2 |

The same exact candidate passed a synthetic stratified gate with 20 measured
samples per available operation after warm-up:

| Entries | `limit=1` first / continued p95 | `limit=100` first / continued p95 | `limit=500` first / continued p95 | max entries scanned |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 1,692 us / n/a | 1,308 us / n/a | 1,559 us / n/a | 1 |
| 100 | 1,309 us / 2,474 us | 5,338 us / n/a | 4,784 us / n/a | 100 |
| 1,000 | 3,479 us / 3,575 us | 6,485 us / 11,154 us | 26,277 us / 29,810 us | 1,000 |
| 10,000 | 35,435 us / 30,646 us | 26,588 us / 26,012 us | 45,684 us / 41,730 us | 10,000 |

All measured current-vault and stratified calls used the candidate's default
SQLite telemetry sink, so the latency samples include synchronous sink cost.
The current-vault run retained all 440 measured plus four setup `tool.call`
rows and parsed all 450 persisted event bodies. The stratified run retained all
418 measured plus twelve setup `tool.call` rows and parsed all 436 persisted
event bodies. After the harness removed the temporary sink table, a real
post-start telemetry write failure emitted the bounded degradation warning and
still returned a valid 211-byte SDK result in 674 us.

All `ls` samples reported zero file-content bytes scanned. The aggregate
reports retained no vault path, directory identity, entry name, cursor, or
content. A two-millisecond cancellation deadline returned to the client in
2,740 us; the candidate then emitted its server-side completion record in
4,953 us after scanning 965 of 10,000 entries, and the same MCP session
completed a follow-up call. This proves handler return and deferred directory
cleanup within the 100-millisecond bound, but it does not claim process
CPU/RSS/FD teardown.

The accepted installed binary also passed resource-report schema v2. Ten fresh
processes had 23,963 us startup p50, 36,572 us startup p95/max, 2,184 us first
call p50, and 4,679 us first-call p95/max. One long-lived process completed
three exact 100-call batches with four blocking-GC acknowledgements. Historical
single-batch maximum post-GC heap allocation growth from the aligned baseline
was 73,488 bytes; historical maximum stabilized 30-second RSS growth was
3,969,024 bytes; waited lifetime
high-water growth was 4,464,640 bytes; and every sample recovered to exactly 14
file descriptors. During the 60-second idle window, CPU delta was zero, RSS did
not grow, tool-call rows stayed at 301, vault activity stayed at 527 with zero
in-flight work, and the two-tool descriptor surface did not change.

One performance capture started immediately after the multi-minute resource
gate failed with only the aggregate `stratified performance gate failed`
diagnostic. Because the result-shape, count, scan, and byte predicates are
deterministic, while p95 is the only environment-sensitive predicate in that
failure branch, the back-to-back load is the inferred cause, not a directly
observed per-case metric. The quiescent exact-binary run recorded above passed,
as had the same suppressed gate inside `make release`; the failed run remains
superseded evidence rather than being discarded or called green.

Run the same sanitized gates against a built candidate with:

```bash
go run ./cmd/gateway-smoke --gateway-bin <candidate> --obsidian-root <vault> --report-json
go run ./cmd/gateway-smoke --gateway-bin <candidate> --obsidian-root <vault> --performance-json
go run ./cmd/gateway-smoke --gateway-bin <candidate> --obsidian-root <vault> --resource-json
go run ./cmd/gateway-smoke --gateway-bin <accepted-control> --obsidian-root <vault> --resource-json --resource-control
```

## Codex Temp-Profile Proof

For local Codex smoke tests, use a temp `CODEX_HOME` and `codex mcp add` so no
global MCP config is modified. A valid temp-profile setup must prove:

- `codex mcp list --json` shows the gateway as an enabled stdio MCP server;
- `codex mcp get <name>` shows the expected repo-local command and synthetic fixture vault;
- a non-interactive Codex run can discover the exact accepted tool surface and
  call representative current tools from the configured `obsidian` MCP server;
- the configured temp SQLite telemetry database contains corresponding `tool.call` rows.

If the non-interactive Codex run requires an external model call and that call is
not allowed in the current execution context, record the boundary explicitly and
fall back to the local SDK subprocess proof. Do not copy Codex auth material into
a temp profile.

## Machine-Impact Proof

Always-on local use requires proof that the gateway is quiet when idle. Before enabling a launch agent or similar process manager, measure and record:

- idle CPU;
- memory;
- open file descriptors;
- startup duration;
- whether startup scans the whole vault;
- behavior when the vault is large or temporarily unavailable.
