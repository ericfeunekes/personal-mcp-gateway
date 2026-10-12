---
name: testing
description: >
  Use when writing tests, fixing failing tests, adding coverage, choosing what
  or how to test, reviewing test adequacy, speeding up a slow suite, or
  handling flaky tests. Maps code characteristics (pure logic, external
  boundary, trust boundary, interface agreement, journeys, nondeterminism) to
  the proof each needs, and owns when tests run (markers, profiles, stages).
  Not for capacity/latency (skill:performance-testing), live/deployed
  multi-boundary live/deployed verification (skill:live-endpoint-verification),
  or root-causing bugs
  (investigate). For routine behavior-preserving cleanup of test
  code, fixtures, or behavior-bearing test machinery, use
  simplify.
metadata:
  version: "0.2.7"
---

# Testing

## Governing Rule

For each acceptance criterion, answer one question: **what test, at what
boundary, would fail if this criterion were unmet?** If you cannot name that
test, the criterion is not tested yet — no volume of other green tests changes
that. Work the question in both directions: from each criterion to its owning
test, and from each test back to the behavior it would catch breaking.

Determine the correct production behavior from the accepted contract, implement
it, then prove it. Tests are the primary correctness mechanism. Do not add
production fallbacks, readbacks, attestations, lifecycle states, or telemetry
merely to make proof easier — add such behavior only when the product contract
or a demonstrated failure mode requires it.


**A test must be able to fail.** Before trusting a new test, observe it fail:
revert the fix, flip the condition, mutate the constant. If you can't observe
the failure, state why. A test that stays green when the behavior breaks is
ceremony — it looks protective while protecting nothing. This falsifiability
check is the standard every other rule in this file serves.

## Test vs QA

A test lives in the test suite, is runnable locally on demand (CI is optional),
and exercises realistically real infrastructure that the run itself provisions
and tears down — not a shared environment. QA is the separate case where a
real client or person must exercise the accepted behavior at its real boundary;
use `software-development:qa`. It never substitutes for a test, and an artifact
produced to *describe* a run (a checked-in contract document, an evidence file,
an attestation) is not proof of anything.
Issue completion follows the accepted outcome, not the merge boundary. When
deployment or runtime evidence is required to establish that outcome, keep the
same Issue open across the necessary merges and runs. Calling evidence QA does
not remove it from accepted criteria. Optional QA outside those criteria does
not hold completion; distinct defects or recovery outcomes belong in their own
work records. Do not split an Issue merely to make a PR close it.
For safety, trust, or irreversible changes, make merge safe before it happens
through a faithful rehearsal, canary, reversible path, or inert-by-default
shape. Post-merge QA is never the first safety mechanism.

## Not This Skill

| Signal | Route to |
|---|---|
| Probing existing code for bugs/fragility (not under active test development) | `investigate` |
| Behavior-preserving cleanup of test code, fixtures, or behavior-bearing test machinery | `simplify` |
| Standalone "where are we undertested?" audit | `testing-audit` |
| Load, soak, stress, resilience, benchmarking — how fast/far/long | `skill:performance-testing` |
| Verifying a deployed system across endpoint, output, trace, or downstream evidence | `skill:live-endpoint-verification` |
| Client- or person-facing acceptance after tests | `software-development:qa` |

Correctness questions belong here; capacity and latency questions belong in
`skill:performance-testing`. If a load test finds a functional bug, the bug
routes back here.

## Characteristics → Proof

Classify the code by what it does — most code has several characteristics, and
each gets its own proof. State the outcome each proof must reach; the mechanism
listed is the default, not a mandate. When the default is infeasible
(no container image, credential gap, prohibitive runtime), drop to the
next-cheapest proof that still exercises something real, and name the residual
gap out loud — never silently substitute a mock and call the claim covered.

| Characteristic | Proof outcome | Default mechanism |
|---|---|---|
| Pure logic (decisions, transforms, parsers, state machines) | Behavior correct across the input space, edge cases included | Example tests for known cases and error paths; property tests where an invariant holds for all inputs |
| External boundary (HTTP, DB, queue, file, LLM, auth) | Your code behaves correctly at the real seam | The real service running locally, version-matched (container). A fake must earn its place with a capability the real service can't give cheaply — deterministic fault injection, isolated logic coverage — and stays grounded by the real-service test that owns the boundary |
| Trust boundary (parsed documents, user input, external responses, untrusted code) | The runtime check rejects hostile and malformed input at the entrypoint that receives it | Feed real malformed/adversarial artifacts through the real entrypoint — not a mocked unit around the check. The check stays in production; tests prove it works, they never replace it |
| Interface agreement with an independently owned/deployed system | Their actual shape/protocol matches your assumption, and drift surfaces as a failure | Contract test against the real system once, recorded; replay in the suite; re-record on schedule or drift signal |
| Same-repo typed interface | — | The type system IS the contract; skip contract tests. Exceptions: ordering constraints, shared mutable state, invariants that span calls — test those behaviors directly |
| User journey composition | The critical path works end-to-end through real components | Few, deterministic E2E tests of journeys users must complete; never for logic coverage |
| Genuinely nondeterministic output (LLM, stochastic) | Quality holds as a distribution | Invariants + satisfaction rates over N runs. Broaden eval suites from real production traces, never synthetic filler rows; keep conversation source, execution, and scoring separate |
| Reimplementation or port | New implementation preserves observable behavior | Differential testing against the reference implementation on generated inputs, outputs normalized; a mismatch means "they disagree," not automatically "new one is wrong" |
| Prompt/instruction surfaces (prompts, skills, rubrics) | Routing, composition, metadata, and rendered availability are protected | Assert behavior and structure, never prose wording — unless the literal text is the product |
| Code too tangled to test at its seam | A minimal seam exists | Extract pure logic / inject the dependency — smallest structural change that unblocks the test, only when structure actually blocks it |

## Prove at the Boundary You Claim

Name the layer each test actually exercises. A proxy one layer down does not
carry the claim: a unit test is not deployed behavior, an HTTP 200 is not
rendered output, a compile is not a configured runtime, a warm cache is not the
cold path, a helper function is not the production entrypoint, a child
process's parser is not the parent's pipe handling. When a test's layer
diverges from the layer the claim is about, record it as a proof gap — not as
equivalent coverage.

Test output binds to the tree it ran on. If you edit code, tests, or config
while a gate is running, that run can diagnose failures but cannot be final
proof — rerun from the final tree.

## Tests That Lie

Recurring shapes of tests that pass while the behavior they name is broken.
Check new tests against this list; the falsifiability check catches most of
them.

- **Wrong-layer proof.** A green helper test while the shipped producer emits
  something else; a seeded render proving the render function but claimed as
  runtime behavior. For identifier/key/artifact changes, at least one test must
  assert the *literal emitted value* — a test that recomputes the expectation
  with the same changed helper proves only self-consistency.
- **Fixture mirroring.** The expected value is derived from the same code under
  test, so wrong behavior produces matching wrong output. Mutation testing
  (break the code, confirm red) is the detector.
- **No-op fakes.** A fake that returns success while storing nothing lets
  duplicate-claim, status-readback, and existence bugs pass. A fake must
  preserve the semantics that later checks read — and it can miss shape traps
  entirely (a call-recording DB fake passes with `%s` placeholders that the
  real `pg` driver, expecting `$1`, would reject; pin the real dialect).
- **Happy-path lifecycle.** Accepted, rejected, and unknown-outcome are
  distinct transitions, not one boolean. Prove pending state is consumed, stale
  state is rejected, and ambiguous outcomes take the recoverable path — an
  `active || pending` assertion is not a transition proof.
- **One of two mirrored validators.** When the same rule is enforced in two
  runtimes (helper + embedded script, parser + CLI), prove both, and prove a
  rejected input mints no reusable artifact (token, cache entry, output file).

## When Tests Run

The suite you run for a change must be fast without dropping the proof that
owns the change's risk. Speed comes from selection — running the right subset
at the right time — never from replacing the owning real-infrastructure test
with a mock. This section is deliberately mechanism-level; execution is where
defaults pay off.

**Stages and budgets.** Pre-commit: seconds (<30s) — the fast subset touching
what changed. Change validation / PR: minutes (<5min) — everything owning the
change's risks, including boundary tests on real local infra. Full/nightly:
unbounded — the whole suite, slow soaks, mutation runs. A slow test goes to a
later stage because its *proof* belongs there, never as a dumping ground for
tests too slow where they belong — make the test faster or the infra shared.

**Markers and profiles.** Give every test a level marker (`unit`, `boundary`,
`contract`, `e2e`) and an infra marker for what it needs (`docker`, `db`,
`llm`). Define named profiles as marker expressions (fast: units only; change:
everything but slow e2e/soak; full: everything) and select by profile, not by
hand-picked paths. Keep selection visible — profiles named in the command or
CI job, never a config default that silently skips a class of tests, so "the
suite passed" has an unambiguous meaning. Derive markers automatically from
test path and fixture usage rather than manual annotation (which drifts) —
but markers are the mechanism, not directory structure; don't reorganize a
repo's test tree just to enable path-based marking. **Default unmarked
tests into the fast tier** so an unclassified test can never silently escape
every profile. The trigger for this apparatus is cost heterogeneity, not suite
size: a small suite that mixes live-LLM or container tests with pure logic
still needs the infra markers, while a suite that is uniformly fast — whatever
its size — should just run whole.

**Flakes.** Never mask a flaky test with retries; a test that passes on retry
three fails most of the time, and production doesn't retry. Reproduce and fix
the root cause — a test that fails under parallel runs but not sequential ones
is usually reporting a real race. If you must quarantine, quarantine with a
TTL that forces a decision, and never change production behavior to quiet a
flake until evidence traces it to a production defect.

**Coverage.** Diff coverage on new code is a useful change-time gate; total
coverage is a trend to watch, not a target to chase. Mutation testing is the
real suite-quality check and runs on the slow tier for critical modules.

## Building a Test Plan

Scale to scope. A single function with one or two characteristics needs no
plan — pick the proof from the table and write it. A multi-boundary feature
needs a short plan naming: which characteristics are present, the proof for
each, and ordering (contract artifacts before the boundary tests that consume
them; independent proofs in parallel).

Every meaningful risk gets exactly one **owning** proof layer — others may
touch it, one is responsible. Prove the primary durable path separately from
auxiliary paths (UX, observability); the auxiliary path is never the proof of
the primary. For changed code: characterize the change, trace the blast radius
through callers, check what's already covered, and map the gaps to the table.
When gaps are many, order by blast radius × how silently the failure would
ship. If proving something requires mocking beyond what's already injected, or
patching internals, the structure is the defect — extract the seam first.

## Spec Consultation

When invoked with a spec instead of code, map each acceptance criterion to the
characteristics table and return a short testing-notes block: characteristics
found (with the AC that implies each), proof per characteristic, and 2–3
sentences on ordering and flakiness risk. The plan document owns the AC list
and the AC→test mapping; this skill supplies the proof shapes.

## Coverage Verification (reviewing)

For each acceptance criterion, ask: which test fails if this is unmet — and is
that test at the layer the claim is about? For each test, ask: could the
implementation change incorrectly and this still pass? Flag tests asserting
implementation detail over contract, risks with no owning layer, and
identifier/artifact changes with no literal-emitted-value assertion. A test
that exists but can't fail is worse than a missing test — it manufactures
confidence.
