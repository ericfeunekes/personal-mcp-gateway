---
title: "Local Release Runbook"
status: draft
purpose: "Move a landed gateway commit into the supervised local runtime with bounded verification and rollback."
covers:
  - Makefile
  - scripts/release-local.sh
  - scripts/release-activation.sh
  - scripts/update-local.sh
  - scripts/verify-live.sh
  - cmd/gateway-smoke/
  - cmd/release-activation/
  - internal/releaseactivation/
---

# Local Release Runbook

## Public Contract

The four agent-facing release commands are:

```bash
make release
make release-status
make release-accept RELEASE_ID=<full-id>
make release-rollback RELEASE_ID=<full-id>
```

`make release` passing local gates means the exact candidate is locally ready
and `pending`; it does not mean the candidate is accepted. The successful fast
path is:

1. Run `make release` and retain the printed full release ID.
2. Complete the applicable change-scoped proof selected from
   `docs/TESTING.md`. For mixed changes, all applicable rows are cumulative.
   Use the smallest set of real journeys that could falsify the behavior changed
   by this release. Refresh and inspect connector metadata
   only when tool names, schemas, descriptions, or annotations changed. Reuse
   accepted activation proof for unaffected mutation, native-document, and
   lifecycle surfaces; do not replay those journeys as general release
   ceremony.
3. Run `make release-accept RELEASE_ID=<full-id>` only after the selected proof
   succeeds. If it does not complete, classify it using the dispatch
   evidence required by `docs/TESTING.md`. Roll back only when telemetry,
   relevant postconditions, installed identity, or health/readiness implicates
   the candidate or installed runtime. An unrelated ChatGPT or model failure is
   recorded separately and does not block acceptance of a journey that already
   proved the changed boundary. After rollback, require a prior-contract call
   only when the connector contract changed or the failure crossed that
   boundary; otherwise the restored hash, ready runtime, and relevant
   postconditions close the rollback.

`make release-status` is the diagnostic and interruption-recovery path, not a
mandatory fast-path step. It prints bounded state and exact next-command records
without paths, credentials, environment values, or private manifest data. A
missing, shortened, stale, or mismatched ID never authorizes a terminal effect.

## Release And Recovery Behavior

`make release` does not fetch or modify Git history. It refuses a dirty tree,
then performs these gates in order:

1. Run the canonical uncached `make test` suite.
2. Build `.build/personal-mcp-gateway` and the release controller with CGO
   enabled, VCS stamping disabled, and path trimming enabled.
3. Start that exact candidate over MCP stdio with telemetry disabled and require
   `resolve(.)` to report an existing directory under the configured synthetic
   or real vault root.
4. Run the sanitized current-vault and stratified performance gate against the
   same candidate, including default SQLite telemetry cost, response/scan
   bounds, cancellation, and fail-open sink degradation.
5. Run the sanitized process-resource gate against the same candidate: ten
   cold processes, three exact 100-call batches with blocking-GC heap and
   stabilized RSS/FD checks, lifetime high-water validation, and a 60-second
   idle window with no tool or vault activity.
   Each report's stdout is captured through a 1 MiB bound plus one-byte
   overflow sentinel. The capture bound does not become a process file-size
   limit on Go compiler, linker, runtime, or other private artifacts.
6. Confirm the candidate did not change during the smoke, then recheck that
   `HEAD` is unchanged and the working tree is still clean.
7. Copy and hash the already-built release controller, the candidate, and the
   previous installed executable when present into the fixed per-user transaction
   slot, and durably publish `prepared` before changing `GATEWAY_BIN`.
8. Through that pinned controller, stage the candidate beside `GATEWAY_BIN`,
   atomically replace it, restart the captured services in Obsidian-then-YNAB
   order, and require the installed SHA-256 and each service's tunnel `/healthz`
   and `/readyz` checks to pass. YNAB participates only when loaded, including a
   loaded job whose process has exited; release never installs an absent service.
9. Record `pending` and print the full release ID plus exact accept and rollback
   commands. Retain the immutable candidate, pinned controller, and previous
   runtime until one exact terminal command proves its outcome.

If the process stops after `prepared`, rerun `make release`. It dispatches to
the pinned controller, reuses the same ID and immutable candidate, and skips
tests, build, and prepare. `accepting` and `rolling_back` resume only through the
same exact-ID terminal command. A second fresh release is blocked until the slot
is clear.

Rollback restores and verifies the exact previous executable when one existed.
It restarts the same captured service set, not a newly discovered set.
For a failed first installation, it first unloads and confirms absence of the
LaunchAgent, then removes and confirms absence of the candidate target. It
preserves the plist/configuration; run `make install-launchagent` before a later
release if the job is no longer loaded. If recovery cannot be confirmed, the
transaction and remaining evidence stay active rather than reporting success.

A failed candidate whose rollback is confirmed reports the fixed
`rolled_back` record, naming only the fixed step that failed (for example
`launch agent installation` or `readiness`); it never restates cause detail.
A private host-effect adapter invocation (launchctl restart/bootout/print, an
install/uninstall adapter, or new-service cleanup) that itself fails, times
out, or exits nonzero reports the fixed `host_effect_failed` record: the same
kind of fixed step name plus a coarse `timeout`, `exit_status=<n>`, or
`failed` class, never child stdout/stderr, paths, or environment data. `make
update`'s post-fetch branch/tree/HEAD/fast-forward checks report the fixed
`update_failed` record on any check failure. `recovery_unconfirmed` is now
reserved for the narrower case where the transaction's outcome genuinely
cannot be determined, such as `launchctl print` itself failing while
confirming a rollback or an unload.

The summary and status records contain only bounded state, full release ID, and
short commit/hash identity. They do not print the vault root, target path,
tunnel identifier, runtime key, wrapper/config fingerprints, or child command
diagnostics.

The stable dispatcher is the sole public formatter boundary. It captures the
controller's stdout and stderr in private `0600` channels, caps each at 64 KiB,
and relays the completed byte streams unchanged. It retries selection once only
when the controller returns the byte-exact pre-effect `authority_mismatch`
record; a child start failure and every other result are final. Release gates
suppress test, build, smoke, fetch, preflight, and child diagnostics. A
successful release therefore prints only the three pending records. If `make release`
encounters `pending`, `accepting`, or `rolling_back`, stdout is empty and stderr
contains the fixed `state_conflict` record, active identity, and only the legal
next command records for that state. Direct Make failures may append Make's own
bounded target/exit diagnostic after the script's exact record stream.

## Source Update

The routine path after code lands is:

```bash
make update
```

`make update` owns source synchronization. It refuses to run outside `main` or
with source changes present and fetches `origin/main` without holding the
release lock because fetch does not mutate the checkout. It then acquires the
same fail-fast lifecycle lock used by release preparation, revalidates that the
transaction is clear and the branch/tree/HEAD are unchanged. Before acquiring
the lock it resolves the fetched commit to a full object ID; under the lock it
verifies that immutable commit object, merges that exact ID with
`merge --ff-only`, and verifies `HEAD` is the same ID. It never merges mutable
`FETCH_HEAD`. The total lock-held update and every Git child are deadline
bounded. It releases the lock before invoking the release script directly, so
a direct `make update` failure has one Make diagnostic suffix. It never deploys local-only
commits, rebases, creates a merge commit, or discards local work.

## One-Time Setup

Copy `.env.example` to ignored `.env.local` and set the local tunnel, vault, and
runtime values. In particular, `make release` requires an absolute
`GATEWAY_BIN`; the default example uses:

```bash
GATEWAY_BIN="$HOME/.local/bin/personal-mcp-gateway"
```

Release deliberately uses this checkout's `.env.local`, because that is the
file the LaunchAgent wrapper consumes. It rejects another environment-file
override and parses the file as bounded configuration data rather than shell
code. Only the keys documented in `.env.example` plus the release readiness and
health-file overrides are accepted; values may be unquoted or single/double
quoted, and the only expansion is a leading `$HOME` or `${HOME}`. Commands,
substitutions, escapes, continuations, duplicate or unknown keys, oversized
files, and oversized lines fail closed with the fixed `release_config` record.
Release strips tunnel credentials before invoking tests/builds/smokes and
requires the candidate and installed binary to be distinct absolute regular
paths.

Install the repo-local tunnel client as described in
`docs/runbooks/openai-tunnel.md`, then install or refresh the LaunchAgent through
the lock-aware Make target:

```bash
make install-launchagent
```

The LaunchAgent may initially retry while `GATEWAY_BIN` is absent. The first
successful `make release` installs it and restarts the same job.

For YNAB, configure private `.env.ynab.local` from `.env.ynab.example`, using
separate tunnel credentials and the same absolute `GATEWAY_BIN`. Release the
YNAB-capable binary before `make install-launchagent SERVER=ynab`. This selected
installation is a clear-state operation; it waits for YNAB readiness and unloads
the newly added job if installation fails, retaining its configuration. YNAB
restart, verification, and uninstall likewise use `SERVER=ynab`. Ordinary
administrative targets continue to select Obsidian by default.

## Supporting Targets

```bash
make help
make test
make build
make restart
make verify-live
make install-launchagent
make uninstall-launchagent
```

`make build` writes only the ignored candidate; it does not change the live
runtime. `make restart`, `make install-launchagent`, and
`make uninstall-launchagent` dispatch through the release controller, acquire
the same lock, require a clear transaction, and invoke private narrow host-effect
adapters while retaining the lock. Do not invoke files under `scripts/internal/`
directly; there are no public install/uninstall script alternatives.
`make verify-live` checks the loaded LaunchAgent and the current loopback tunnel
liveness/readiness endpoints without changing state. Release and rollback own
their required readiness checks, so `make verify-live` is diagnostic and
one-time evidence rather than an extra transaction transition.

## State And Authority

Release state uses one non-configurable per-user locator derived from the
effective user's passwd home, not caller `HOME` or `.env.local`. The fixed slot
contains a permanent advisory lock and, while active, a private versioned
manifest plus immutable candidate, optional previous binary, and the pinned
controller. Do not edit, move, or delete these files manually. Status and
terminal commands select the pinned controller and revalidate its hash under the
lock, so mutable checkout, `.build`, or environment drift cannot silently replace
the authority that created the transaction. Wrapper/config fingerprint drift
fails closed because the supervised runtime can no longer be proven.
New transactions use manifest version 3 with per-service descriptors under the
same single slot. Existing pending transactions finish with their pinned
controller. Both services' configuration and wrapper fingerprints are checked
before terminal operations; do not edit either during an active release.

## Proof Boundary

A locally successful `make release` proves only that the checked-out commit
passed the repo suite; the exact candidate served MCP stdio and resolved the
configured root; the current-vault, stratified-performance, cold-process,
repeated-call resource, and 60-second idle gates passed; the installed bytes
match the candidate; and the restarted tunnel became live and ready. It
deliberately ends `pending`.

Acceptance requires the applicable change-scoped proof in `docs/TESTING.md`,
followed by the exact-ID accept command. A lifecycle-only change uses the
process/crash/concurrency suite and `make verify-live`; it needs a fresh
authenticated tool call only when the connector boundary also changed. These
checks prove process-crash recovery on the tested machine; they do not prove
power-loss durability, sleep/wake recovery, multi-day soak behavior, every
prompt formulation, or future-machine/vault performance.

Run the isolated first-install check with the opt-in command in
`docs/TESTING.md`. It creates only a randomized temporary LaunchAgent, target,
and release store; success requires real `launchctl print` absence, target
removal, and a clear transaction after rollback.

The current accepted proof record is maintained in `docs/TESTING.md`. Repeat
cell 1 after changing lifecycle policy, dispatcher capture,
installed-target replacement, or supervision bindings. Add cell 2 only when
the connector boundary also changed. Advertised MCP changes select their
applicable cell-2 rows.
