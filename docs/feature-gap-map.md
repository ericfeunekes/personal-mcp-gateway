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
| GAP-GW-007 | proof | `docs/gateway.md` / `docs/TESTING.md` / `docs/runbooks/local-release.md` | Pending release activation is not accepted until the executable lifecycle/process proof is green, an installed candidate is proven recoverable through pending-to-rollback, and a separate authenticated metadata refresh/model-selected journey is followed by exact-ID acceptance. Local readiness alone does not close this gap. |
| GAP-OBS-001 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | `read`, `read_many`, and `grep` are not implemented. |
| GAP-OBS-002 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | ChatGPT and Codex proof covers the current `ls`/`resolve` surface only; the expanded agent workflow is not proven live. |
| GAP-OBS-003 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Root confinement, denial, read-only, and sanitized-error proof is not extended to content, batch, cursor, reference, and graph operations. |
| GAP-OBS-004 | spike | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Full-vault grep, backlink, and path-discovery latency, scan work, response size, and freshness trade-offs are not measured. The bounded exercise/health/marathon spike is complete. |
| GAP-OBS-005 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Existing tunnel/runtime proof remains valid, but live metadata refresh and representative model-selected calls must be repeated after the tool list expands. |
| GAP-OBS-006 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | `links`, the scoped request-local path catalog, and outbound `traverse` are not implemented. |
| GAP-OBS-007 | spike | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | The full-vault activation gate for live request-local `backlinks` and `path_between` has not been run or passed; the tools are neither implemented nor advertised. |
| GAP-OBS-008 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Per-tool safe telemetry summaries for read, grep, batch, links, traversal, backlinks, and path discovery are not implemented. |
| GAP-OBS-009 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Safe telemetry summaries for newly activated retrieval and graph tools have not been proven in JSONL and SQLite. |
| GAP-OBS-010 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Live request-local `backlinks` and `path_between` are not implemented for pre-activation benchmark and proof. |
| GAP-OBS-011 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | The currently advertised `resolve` and `ls` tools do not yet implement stored-spelling/NFC canonical identity, stateless `ls` cursors, common coverage/response budgets, or domain-owned safe telemetry summaries. Their activation also remains blocked by `GAP-GW-007`; no live tool proof may be relabeled as accepted while the release handshake is unproven. |
