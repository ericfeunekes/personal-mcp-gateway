---
title: "Local Release Runbook"
status: draft
purpose: "Move a landed gateway commit into the supervised local runtime with bounded verification and rollback."
covers:
  - Makefile
  - scripts/release-local.sh
  - scripts/update-local.sh
  - scripts/verify-live.sh
  - cmd/gateway-smoke/
---

# Local Release Runbook

## Contract

The routine path after code lands is:

```bash
make update
```

`make update` owns source synchronization. It refuses to run outside `main` or
with source changes present, fetches `origin`, fast-forwards to `origin/main`,
requires local `HEAD` to exactly match that remote-tracking commit, and invokes
`make release`. It never deploys local-only commits, rebases, creates a merge
commit, or discards local work.

Use this when the desired commit is already checked out:

```bash
make release
```

`make release` does not fetch or modify Git history. It refuses a dirty tree,
then performs these gates in order:

1. Run the canonical uncached `make test` suite.
2. Build `.build/personal-mcp-gateway` with CGO enabled, VCS stamping disabled,
   and path trimming enabled.
3. Start that exact candidate over MCP stdio with telemetry disabled and require
   `resolve(.)` to report an existing directory under the configured synthetic
   or real vault root.
4. Confirm the candidate did not change during the smoke, then recheck that
   `HEAD` is unchanged and the working tree is still clean.
5. Copy the prior installed executable to a temporary rollback file, stage the
   candidate beside `GATEWAY_BIN`, and require the smoked, staged, and installed
   SHA-256 values to match before atomically renaming it into place.
6. Remove the previous health-URL file, restart the tunnel LaunchAgent, confirm
   the loaded job points at this checkout's tunnel wrapper, and wait up to 45
   seconds for tunnel `/healthz` and `/readyz`.
7. On failure, restore the previous executable, restart it, and attempt bounded
   readiness verification again. The rollback copy is deleted only after
   recovery is confirmed; otherwise it is retained beside the configured
   target for manual recovery. A failed first installation is removed when no
   previous executable exists.

The successful release summary names only the commit and a shortened binary
hash. It does not print the vault root, tunnel identifier, runtime key, or
installed path.

## One-Time Setup

Copy `.env.example` to ignored `.env.local` and set the local tunnel, vault, and
runtime values. In particular, `make release` requires an absolute
`GATEWAY_BIN`; the default example uses:

```bash
GATEWAY_BIN="$HOME/.local/bin/personal-mcp-gateway"
```

Release deliberately uses this checkout's `.env.local`, because that is the
file the LaunchAgent wrapper consumes. It rejects another environment-file
override, strips tunnel credentials before invoking tests/builds/smokes, and
requires the candidate and installed binary to be distinct absolute regular
paths.

Install the repo-local tunnel client as described in
`docs/runbooks/openai-tunnel.md`, then install or refresh the LaunchAgent:

```bash
make install-launchagent
```

The LaunchAgent may initially retry while `GATEWAY_BIN` is absent. The first
successful `make release` installs it and restarts the same job.

## Supporting Targets

```bash
make help
make test
make build
make restart
make verify-live
```

`make build` writes only the ignored candidate; it does not change the live
runtime. `make restart` restarts the existing LaunchAgent without rebuilding.
`make verify-live` checks the loaded LaunchAgent and the current loopback tunnel
liveness/readiness endpoints without changing state.

## Proof Boundary

A successful local release proves the checked-out commit passed the repo suite,
the exact candidate served MCP stdio and resolved the configured root, the
installed bytes match the candidate, and the restarted tunnel became live and
ready. It does not prove a remote ChatGPT or Codex model selected a tool after
the restart. Run and record a model-driven live-surface smoke separately when a
change affects tool registration, schemas, connector metadata, or the remote
OpenAI boundary.
