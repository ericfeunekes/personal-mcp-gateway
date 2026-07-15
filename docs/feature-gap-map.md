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
| GAP-GW-002 | proof | `docs/gateway.md` / `docs/runbooks/openai-tunnel.md` | Tunnel startup, polling, app installation, connector discovery, ChatGPT model-driven `ls`, and Codex model-driven `resolve` are proven; a ChatGPT-web-specific `resolve` call is not. |
| GAP-GW-003 | proof | `docs/gateway.md` | Current `launchd` readiness, bounded idle impact, and automatic crash recovery are proven; a multi-day soak and sleep/wake recovery cycle are not measured. |
| GAP-GW-006 | proof | `docs/gateway.md` / `docs/TESTING.md` | Sanitized model-driven ChatGPT `ls` and Codex `resolve` telemetry have been harvested; ChatGPT-web-specific `resolve` telemetry has not. Local SDK subprocess and HTTP tests prove the broader server-side matrix only. |
| GAP-OBS-001 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | `read`, `read_many`, and `grep` are not implemented. |
| GAP-OBS-002 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | ChatGPT and Codex proof covers the current `ls`/`resolve` surface only; the expanded agent workflow is not proven live. |
| GAP-OBS-003 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Root confinement, denial, read-only, cursor, and sanitized-error proof covers the current `resolve`/`ls` surface; it is not extended to content, batch, reference, and graph operations. |
| GAP-OBS-004 | spike | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Full-vault grep, backlink, and path-discovery latency, scan work, response size, and freshness trade-offs are not measured. The bounded exercise/health/marathon spike is complete. |
| GAP-OBS-005 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Existing tunnel/runtime proof remains valid, but live metadata refresh and representative model-selected calls must be repeated after the tool list expands. |
| GAP-OBS-006 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | `links`, the scoped request-local path catalog, and outbound `traverse` are not implemented. |
| GAP-OBS-007 | spike | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | The full-vault activation gate for live request-local `backlinks` and `path_between` has not been run or passed; the tools are neither implemented nor advertised. |
| GAP-OBS-008 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Descriptor-owned safe telemetry summaries are implemented for `resolve` and `ls`; summaries for read, grep, batch, links, traversal, backlinks, and path discovery are not implemented. |
| GAP-OBS-009 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | `resolve`/`ls` summaries have local JSONL and SQLite proof; summaries for newly activated retrieval and graph tools have not been proven. |
| GAP-OBS-010 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Live request-local `backlinks` and `path_between` are not implemented for pre-activation benchmark and proof. |
| GAP-OBS-011 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Stored-spelling/NFC identity, stateless `ls` cursors, coverage/response budgets, descriptor authority, candidate smoke, domain-owned telemetry, and cached current-vault performance are locally proven; ten cold calls, RSS/FD/CPU and 60-second idle observation, refreshed authenticated metadata/model-selected continuation, and exact-candidate release acceptance remain. |
