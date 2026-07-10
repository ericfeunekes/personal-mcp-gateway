---
title: "Feature Gap Map"
status: draft
purpose: "Track current implementation, proof, decision, and spike gaps for repo-native planning."
covers:
  - docs/requirements/
  - docs/gateway.md
  - docs/obsidian.md
---

# Feature Gap Map

| Gap ID | Kind | Owning doc | Gap |
| --- | --- | --- | --- |
| GAP-GW-002 | proof | `docs/gateway.md` / `docs/runbooks/openai-tunnel.md` | OpenAI Secure MCP Tunnel stdio startup, control-plane polling, ChatGPT app installation, connector `initialize`/`tools/list`, and a bounded model-driven `ls` call are proven; model-driven `resolve` is not. |
| GAP-GW-003 | proof | `docs/gateway.md` | Supervision-grade readiness, tunnel readiness, and idle-impact readiness are not proven. |
| GAP-GW-005 | proof | `docs/gateway.md` / `docs/runbooks/openai-tunnel.md` | `launchd` LaunchAgent startup, manual restart recovery, and post-restart connector metadata refresh are proven; automatic crash recovery and idle-impact readiness are not proven. |
| GAP-GW-006 | proof | `docs/gateway.md` / `docs/TESTING.md` | Sanitized model-driven ChatGPT `ls` telemetry has been harvested; model-driven `resolve` telemetry has not. Local SDK subprocess and HTTP tests prove the broader server-side matrix only. |
| GAP-OBS-001 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | `read`, `grep`, `search`, and `stat` schemas and handlers are not implemented. |
| GAP-OBS-002 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | ChatGPT accepts the `obsidian` server, discovers simple `ls` and `resolve` names, displays both as read-only, and completes a bounded model-driven `ls` call; model-driven `resolve` is not proven. |
| GAP-OBS-003 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Root confinement and path-denial proof must be extended when future read, search, grep, and stat tools are implemented. |
| GAP-OBS-004 | spike | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Search performance and idle impact over the real vault have not been measured. |
| GAP-OBS-005 | proof | `docs/requirements/obsidian-filesystem-tools.md` | OpenAI tunnel foreground and LaunchAgent startup, ChatGPT app installation, live metadata refresh, and a bounded ChatGPT `ls` call are proven for the stdio profile; model-driven `resolve` and idle-impact readiness are not proven. |
