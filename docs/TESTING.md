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
| Read-only guarantee | Integration test with before/after fixture-vault snapshot | Any Obsidian tool change |
| Search and listing limits | Large fixture tests with timeout, depth, byte, result, cancellation, oversized-literal streaming, bounded-evidence, and regex-line-cap assertions | Any traversal or search change |
| Structured telemetry | SQLite and JSONL proof matrix covering event families, sanitized identifiers, sink degradation, and no raw path leaks | Any audit or tool-call behavior change |
| Local release transaction lifecycle | Executable state/event matrix plus process tests for locking, crash-boundary reconciliation, exact-hash accept/rollback, first-install unload, recovery-artifact retention, and installed-service pending-to-terminal journeys | Any change to release, update, rollback, acceptance, or supervised-runtime activation behavior |
| Obsidian server tool names in ChatGPT | Live smoke test through OpenAI Secure MCP Tunnel | Before treating connector compatibility as settled |
| Minimal machine impact | Local process observation for idle CPU, memory, file descriptors, startup behavior, and no whole-vault startup scan | Before always-on usage |

## Expected Commands

Canonical test command:

```bash
make test
```

The target discovers every Go package, runs ordinary packages together so Go
can retain package-level concurrency, and then runs `./cmd/gateway-smoke` and
`./scripts` as separate sequential stages. Every stage uses `go test -count=1`,
so the release gate executes the process and boundary tests rather than reusing
prior successful results. The isolated stages prevent the performance/resource
smoke and release watchdog suites from competing with heavyweight packages or
with each other while their production thresholds remain unchanged.

When running inside a restricted agent sandbox that cannot write the default Go
build cache, keep the build cache repo-local:

```bash
GOCACHE=$(pwd)/.gocache make test
```

The canonical suite includes the existing gateway proof below. The pending
release implementation must add the stated lifecycle coverage before it is
treated as accepted:

- config validation and loopback bind rejection tests;
- root-confined filesystem adapter tests for traversal, absolute paths, hidden entries, symlink traversal, limits, cancellation, and read-only behavior;
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
- no raw vault root, host path, note path, note content, tunnel credential, or
  token material in JSONL text, SQLite indexed columns, or SQLite `body_json`.

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
GOCACHE=$(pwd)/.gocache go test -count=1 ./cmd/gateway-smoke -run '^TestPhase2ResourceProbeExercisesBuiltFiveToolCandidate$' -v
```

The resource proof must observe overlapping request-local pools above one
eight-worker ceiling, bounded active and reserved work, cancellation isolation,
immediate FD/vault quiescence, and a successful same-session follow-up.

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

The release proof contract is split into three current-state cells:

1. Run the canonical merge proof, including the executable lifecycle matrix and
   process/crash/concurrency coverage:

   ```bash
   GOCACHE=$(pwd)/.gocache go test -count=1 ./internal/releaseactivation ./cmd/release-activation ./scripts
   GOCACHE=$(pwd)/.gocache go test -race -count=1 ./internal/releaseactivation ./cmd/release-activation ./scripts
   make test
   git diff --check
   ```

2. On an isolated synthetic LaunchAgent and then the installed service, prove a
   release reaches `pending`, rejects missing/stale/wrong IDs without changing
   state, and exact rollback restores the previous hash and ready runtime. For a
   first installation, prove the job is unloaded before the candidate target is
   removed while its plist/configuration remains available for a later install.
   The isolated macOS drill is opt-in and uses a randomized label plus temporary
   target/store paths:

   ```bash
   make build-release-controller
   RUN_LIVE_RELEASE_FIRST_INSTALL=1 GOCACHE=$(pwd)/.gocache \
     go test -count=1 ./internal/releaseactivation \
     -run '^TestLiveFirstInstallLaunchAgent(Rollback|Helper)$'
   ```
3. In the authenticated OpenAI surface, refresh metadata for server `obsidian`
   and observe exactly `grep`, `ls`, `read`, `read_many`, and `resolve`, all
   read-only. In a fresh model run, require exactly `grep` -> `read_many` ->
   continued `read_many`: both batch calls use the same three ordered requests
   and `max_bytes=300`, the first omits a cursor, and only the continuation
   supplies the returned cursor. Only then run exact-ID acceptance and prove the
   candidate hash remains installed, ready, and the transaction returns to
   `clear`. Later graph phases replace this prerequisite journey with their own
   newly activated representative calls.

Record sanitized release identity and hash prefixes, the authenticated surface,
metadata observation, selected tool/journey, and terminal outcome. Do not record
prompts, note names or content, vault paths, credentials, raw environment data,
or private manifest fields. The installed drills establish a representative
activation transaction, not power-loss durability, sleep/wake recovery,
multi-day soak behavior, every prompt formulation, or future-vault performance.

### Current accepted five-tool Phase 2 proof

On 2026-07-17, implementation commit `d74fcd3ba1b1`, installed candidate hash
prefix `2de6c5f23082`, and release prefix `725425303f84` passed `make test`, the
exact-candidate functional v3, performance v3, resource v5, and cross-report
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
strata remained below 44 ms. Resource v5 recorded 8,884,224 bytes maximum
high-water RSS growth, 2,371,584 bytes stabilized 30-second RSS growth, 185,240
bytes heap growth, exact descriptor recovery after 312 calls, and zero CPU,
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
three exact 100-call batches with four blocking-GC acknowledgements. Maximum
post-GC heap allocation growth from the aligned baseline was 73,488 bytes;
maximum stabilized 30-second RSS growth was 3,969,024 bytes; waited lifetime
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
```

## Codex Temp-Profile Proof

For local Codex smoke tests, use a temp `CODEX_HOME` and `codex mcp add` so no
global MCP config is modified. A valid temp-profile setup must prove:

- `codex mcp list --json` shows the gateway as an enabled stdio MCP server;
- `codex mcp get <name>` shows the expected repo-local command and synthetic fixture vault;
- a non-interactive Codex run can discover the exact five-tool surface and call representative current tools from the configured `obsidian` MCP server;
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
