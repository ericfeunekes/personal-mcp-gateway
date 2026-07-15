---
title: "OpenAI Tunnel Runbook"
status: draft
purpose: "Run the local Obsidian MCP server through OpenAI Secure MCP Tunnel without storing tunnel secrets in committed files."
covers:
  - scripts/run-obsidian-tunnel.sh
  - scripts/run-obsidian-mcp-stdio.sh
  - tools/tunnel-client/
---

# OpenAI Tunnel Runbook

## Secret Location

Put the OpenAI tunnel runtime key in repo-local `.env.local`:

```bash
CONTROL_PLANE_API_KEY=...
```

`.env.local` is ignored by git and should stay local to this machine. Do not put
the key in docs, prompts, scratch files, generated tunnel profiles, or committed
config.

The run script passes the key to `tunnel-client` through
`env:CONTROL_PLANE_API_KEY`, so the generated profile stores only an environment
variable reference, not the key value.

## Foreground Run

From the repo root:

```bash
scripts/run-obsidian-tunnel.sh
```

The script:

- loads `.env.local`;
- validates `CONTROL_PLANE_TUNNEL_ID`, `CONTROL_PLANE_API_KEY`, and
  `OBSIDIAN_ROOT`;
- generates a local `obsidian-stdio` tunnel-client profile under the temp
  directory;
- starts the repo-local `tools/tunnel-client/tunnel-client`;
- launches the Obsidian MCP server through `scripts/run-obsidian-mcp-stdio.sh`;
- stores gateway telemetry at the configured SQLite path, defaulting to the
  user application-support directory.

Use foreground mode for short manual smoke tests. Use the LaunchAgent below when
ChatGPT connector testing must survive the current shell session. The current
LaunchAgent profile passed bounded idle-impact and automatic-recovery proof on
2026-07-10 and is suitable for always-on use under that measured contract.

## LaunchAgent Run

For ChatGPT testing that must survive the current shell session, install and
start the user LaunchAgent:

```bash
make install-launchagent
```

The LaunchAgent label is
`com.ericfeunekes.personal-mcp-gateway.obsidian-tunnel`. It runs
`scripts/run-obsidian-tunnel.sh`, parses the allowlisted values in ignored
`.env.local` as bounded data, and writes tunnel-client stdout/stderr under
`~/Library/Logs/personal-mcp-gateway/`.

The tunnel and MCP stdio wrappers never shell-source `.env.local`; command
substitutions, shell commands, duplicate/unknown keys, oversized records, and
unsupported expansion are rejected before runtime startup. `make
install-launchagent` performs the same validation before building the release
controller or invoking the private install adapter.

On 2026-07-02, the LaunchAgent was installed and started successfully. Observed
proof: launchd reported the service `running`, the tunnel health/admin endpoint
returned HTTP 200, metrics reported `readiness` and `liveness` as `1`, and
control-plane poll metrics advanced.

Stop and remove it with:

```bash
make uninstall-launchagent
```

Install, restart, and uninstall are clear-only lifecycle operations. Their Make
targets acquire the release lock and invoke private narrow LaunchAgent adapters;
do not invoke files under `scripts/internal/` directly. A first-install rollback
unloads the job before removing the unproven target but preserves its
plist/configuration, so run `make install-launchagent` before a later release if
the job remains unloaded.

Once the LaunchAgent exists, use the release flow rather than manually
rebuilding its configured gateway binary. `make release` deploys the current
clean commit; `make update` fast-forwards clean local `main` from GitHub first.
See `docs/runbooks/local-release.md`.

## Release Activation Proof

Tunnel liveness/readiness and authenticated model proof are separate boundaries.
The release fast path is:

```bash
make release
# Refresh server `obsidian` metadata, observe exactly current tools `ls` and
# `resolve`, and have the model select two one-item shallow root `ls` pages by
# continuing the first result's cursor with the same query.
make release-accept RELEASE_ID=<full-id>
```

If metadata refresh or the model-selected journey fails, run the exact rollback
command printed by release, refresh authenticated metadata back to the prior
schema, and make one successful prior-contract call:

```bash
make release-rollback RELEASE_ID=<full-id>
```

`make release` must end `pending` after local readiness and retain the previous
runtime. `make release-status` is available after interruption or for bounded
diagnostics; it is not an extra mandatory step in the successful flow. Never
accept based only on `/healthz`, `/readyz`, `make verify-live`, local MCP smoke,
or an old model journey. Record only the authenticated surface, server, metadata
observation, selected tool/journey, sanitized release/hash identity, and outcome;
do not record prompts, note names/content, vault paths, credentials, or raw
environment data.

The current `resolve`/`ls` implementation has passed the merge suite, installed
rollback drills, and a fresh authenticated two-page pending-to-accept journey.
The sanitized accepted record is maintained in `docs/TESTING.md`. Repeat the
same boundaries after changing the advertised tools or release lifecycle; the
historical evidence below proves only the original tunnel setup.

## Current Boundary

The stdio tunnel profile is the current live-smoke path because local tunnel
`doctor` passed for stdio and the HTTP profile still needs OpenAI-compatible
OAuth resource metadata before it should be used for ChatGPT connector proof.

## Latest Local Proof

On 2026-07-02, `scripts/run-obsidian-tunnel.sh` started the repo-local
`obsidian-stdio` profile with a valid tunnel runtime key. Observed proof:

- tunnel-client bound its localhost health/admin listener and served the admin
  page and metrics endpoint;
- tunnel-client fetched OpenAI tunnel metadata successfully and started polling
  the OpenAI control plane;
- metrics reported `readiness` and `liveness` as `1`;
- the stdio MCP child process started behind the tunnel profile;
- `tunnel-client doctor` passed profile loading, key reference, MCP command
  executable checks, health listener, and UI checks.

`tunnel-client doctor` skips stdio MCP protocol probing by design. A separate
direct stdio-wrapper probe confirmed `ListTools`, `resolve`, and `ls` work
through `scripts/run-obsidian-mcp-stdio.sh`, but that is local MCP proof rather
than ChatGPT connector proof.

On 2026-07-10, the Personal ChatGPT workspace was associated with the tunnel,
the development app was installed and connected as `Obsidian`, and ChatGPT
successfully refreshed the `ls` and `resolve` metadata through the tunnel.
The actions are displayed as read-only after adding explicit MCP impact
annotations and rebuilding the configured user-level gateway binary. Local
telemetry records the corresponding `initialize` and `tools/list` requests.
Manual LaunchAgent restart recovery is also proven: after `kickstart -k`, the
service returned to `running` and ChatGPT completed another live metadata
refresh through the restarted tunnel.

The first bounded live attempt was interrupted by ChatGPT Work usage limits,
but a later retry completed through the tunnel. At
`2026-07-10T17:23:00.099706Z`, sanitized SQLite telemetry recorded a successful
stdio `tools/call` for `ls` with `path` present, `limit` 5, five returned
entries, and a truncated result. No raw path or entry names were recorded.
This proves model-driven ChatGPT `ls` execution without reading file contents.

Later on 2026-07-10, Codex called `resolve` through the installed `Obsidian` app
with the vault-relative root (`.`). The call returned an existing directory and
sanitized SQLite telemetry recorded `tool=resolve`, `outcome=ok`, a relative
path with zero segments, and no raw host path or note content. This proves a
model-selected `resolve` call through the tunnel from the Codex OpenAI surface;
it is not a ChatGPT-web conversation transcript.

## Idle-Impact Baseline

On 2026-07-10, the existing LaunchAgent had been continuously running for about
3 hours 40 minutes before measurement. After one live tool call, seven idle
samples were collected from `2026-07-10T18:22:59Z` through
`2026-07-10T18:26:00Z` for the tunnel client, its Codex app-server sidecar, and
the gateway child:

- every instantaneous CPU sample was `0.0%`;
- cumulative CPU time increased by 0.13 seconds across all three processes,
  about 0.08% of one core over the window;
- combined RSS remained below 37 MiB;
- open descriptor counts were unchanged at 21, 23, and 12;
- the gateway held zero vault files open at the start and end of the window;
- startup construction uses root validation only, with no whole-vault walk or
  directory read, and the open-file observation is consistent with that path;
- an isolated LaunchAgent using the production tunnel wrapper, `KeepAlive=true`,
  and the production 30-second throttle failed before tunnel startup when given
  a synthetic unavailable root. It exited 1, scheduled bounded retries, kept
  stdout empty, and wrote only a generic error without the configured root or
  another host path. Process-level regression tests cover both production
  wrappers.

This is a bounded always-on idle baseline, not a multi-day leak soak or a
sleep/wake test.

## Automatic Crash-Recovery Proof

At `2026-07-10T18:26:36Z`, the running tunnel-client PID received `SIGKILL`.
No `launchctl kickstart`, reinstall, or other manual recovery action followed.
`launchd` recorded signal 9, incremented the run count, and started a new tunnel
process nine seconds after the fault. The new process recreated both child
processes; its new loopback metrics endpoint reported `liveness=1` and
`readiness=1`.

At `2026-07-10T18:27:24.548116Z`, a second Codex model-driven `resolve` call
completed through the recovered tunnel and produced a successful sanitized
`tool.call` row under the new gateway run. This proves service recovery, not
only process replacement.

The wrapper path-sanitization fix was then projected into the real service with
a second controlled `SIGKILL` at `2026-07-10T18:40:02Z`. `launchd` started the
final-tree tunnel process five seconds later, recreated both children, and
returned to `liveness=1` and `readiness=1`. At
`2026-07-10T18:40:56.147842Z`, another Codex model-driven `resolve` call
succeeded and produced a sanitized row under a third gateway run.

The remaining surface-specific connector gap is a ChatGPT-web-prompted
`resolve` call. The same installed app and tunnel path are proven from Codex,
but that evidence should not be relabeled as ChatGPT-web proof.
