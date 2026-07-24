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
| GAP-GW-002 | proof | `docs/gateway.md` / `docs/runbooks/openai-tunnel.md` | Tunnel startup, polling, app installation, exact five-tool discovery, and ChatGPT model-driven `ls` and core retrieval are proven. Codex `resolve` is historical two-tool evidence; current five-tool Codex/ChatGPT-web `resolve` calls are not proven. |
| GAP-GW-003 | proof | `docs/gateway.md` | Current `launchd` readiness, bounded idle impact, and automatic crash recovery are proven; a multi-day soak and sleep/wake recovery cycle are not measured. |
| GAP-GW-006 | proof | `docs/gateway.md` / `docs/TESTING.md` | Sanitized model-driven ChatGPT `ls`, `grep`, and continued `read_many` telemetry plus historical two-tool Codex `resolve` telemetry have been harvested; current five-tool Codex/ChatGPT-web `resolve` telemetry has not. Local SDK subprocess and HTTP tests prove the broader server-side matrix only. |
| GAP-OBS-002 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | The five-tool core-retrieval workflow is proven live through ChatGPT; outbound and inbound graph workflows are not yet proven. |
| GAP-OBS-003 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Root confinement, denial, read-only, cursor, and sanitized-error proof is complete for the accepted five-tool core surface; equivalent proof is not yet extended to reference and graph operations. |
| GAP-OBS-004 | spike | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Full-vault backlink and path-discovery latency, scan work, response size, and freshness trade-offs are not measured. The bounded exercise/health/marathon spike and activated grep measurements are complete. |
| GAP-OBS-006 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | `links`, the scoped request-local path catalog, and outbound `traverse` are not implemented. |
| GAP-OBS-007 | spike | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | The full-vault activation gate for live request-local `backlinks` and `path_between` has not been run or passed; the tools are neither implemented nor advertised. |
| GAP-OBS-008 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Descriptor-owned safe telemetry summaries are complete for the accepted five-tool core surface; summaries for links, traversal, backlinks, and path discovery are not implemented. |
| GAP-OBS-009 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | The accepted five-tool summaries have local JSONL/SQLite proof plus live model-driven `ls`, `grep`, and continued `read_many` telemetry; graph-tool summaries have not been proven. |
| GAP-OBS-010 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Live request-local `backlinks` and `path_between` are not implemented for pre-activation benchmark and proof. |
| GAP-OBS-011 | blocked implementation | `docs/obsidian.md` / `docs/requirements/obsidian-mutation-tools.md` | Mutation-scoped `stat` plus default-advertised `write`, structured-patch `edit`, `move`, and permanent `delete` are blocked: supporting macOS volumes expose absent-destination no-replace but no existing-name full-source-version compare-and-change namespace effect. GitHub issue #3 must return to phase planning rather than weaken the fingerprint contract. |
| GAP-OBS-012 | blocked proof | `docs/TESTING.md` / `docs/requirements/obsidian-mutation-tools.md` | The exact five-tool mutation delta across descriptors, filesystem race/interruption, sanitized telemetry, resources, release reports, and the authenticated disposable-target journey cannot run until GAP-OBS-011 has an enforceable authority boundary. |
| GAP-OBS-013 | implementation / transport proof | `docs/requirements/obsidian-document-reading.md` / `docs/spikes/issue-4-native-document-transfer.md` | Authenticated ChatGPT interpreted a small connector-returned PDF after accepted one-time materialization consent, but the exact 49,999,999-byte embedded-resource path breached the existing 64 MiB peak and 8 MiB retained-RSS limits. Resource-link or native-hosted delivery remains unproven for the selected below-50-MB target. The bounded document-reading design, handlers, changed-source failure, fixtures, telemetry, and integrated acceptance proof remain unimplemented. |
