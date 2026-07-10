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
| GAP-OBS-001 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | `read`, `grep`, `search`, and `stat` schemas and handlers are not implemented. |
| GAP-OBS-002 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | ChatGPT accepts the `obsidian` server, discovers simple read-only `ls` and `resolve` actions, and completes a bounded model-driven `ls`; Codex model-driven `resolve` is proven, but a ChatGPT-web-specific `resolve` call is not. |
| GAP-OBS-003 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Root confinement and path-denial proof must be extended when future read, search, grep, and stat tools are implemented. |
| GAP-OBS-004 | spike | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Search performance over the real vault has not been measured; current-runtime idle impact is proven. |
| GAP-OBS-005 | proof | `docs/requirements/obsidian-filesystem-tools.md` | Tunnel startup, app installation, metadata refresh, ChatGPT `ls`, Codex `resolve`, bounded idle impact, and automatic crash recovery are proven for stdio; a ChatGPT-web-specific `resolve` call remains. |
