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
ChatGPT connector testing must survive the current shell session. Idle resource
impact still needs measurement before treating the tunnel as permanently
always-on.

## LaunchAgent Run

For ChatGPT testing that must survive the current shell session, install and
start the user LaunchAgent:

```bash
scripts/install-obsidian-tunnel-launchagent.sh
```

The LaunchAgent label is
`com.ericfeunekes.personal-mcp-gateway.obsidian-tunnel`. It runs
`scripts/run-obsidian-tunnel.sh`, reads secrets from ignored `.env.local`, and
writes tunnel-client stdout/stderr under
`~/Library/Logs/personal-mcp-gateway/`.

On 2026-07-02, the LaunchAgent was installed and started successfully. Observed
proof: launchd reported the service `running`, the tunnel health/admin endpoint
returned HTTP 200, metrics reported `readiness` and `liveness` as `1`, and
control-plane poll metrics advanced.

Stop and remove it with:

```bash
scripts/uninstall-obsidian-tunnel-launchagent.sh
```

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
refresh through the restarted tunnel. Automatic crash recovery remains a
separate proof gap.

The first bounded live attempt was interrupted by ChatGPT Work usage limits,
but a later retry completed through the tunnel. At
`2026-07-10T17:23:00.099706Z`, sanitized SQLite telemetry recorded a successful
stdio `tools/call` for `ls` with `path` present, `limit` 5, five returned
entries, and a truncated result. No raw path or entry names were recorded.
This proves model-driven ChatGPT `ls` execution without reading file contents.

Complete first-slice connector proof still requires a model-driven `resolve`
call and its corresponding sanitized SQLite `tool.call` row. Idle-impact and
automatic crash-recovery proof also remain open.
