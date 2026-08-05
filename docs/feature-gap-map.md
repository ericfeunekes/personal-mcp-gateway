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
| GAP-GW-003 | proof | `docs/gateway.md` | Current `launchd` readiness, bounded idle impact, and automatic crash recovery are proven; a multi-day soak and sleep/wake recovery cycle are not measured. |
| GAP-OBS-002 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | The five-tool core-retrieval workflow is proven live through ChatGPT; outbound and inbound graph workflows are not yet proven. |
| GAP-OBS-003 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Root confinement, denial, read-only, cursor, and sanitized-error proof is complete for the accepted five-tool core surface; equivalent proof is not yet extended to reference and graph operations. |
| GAP-OBS-004 | spike | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Full-vault backlink and path-discovery latency, scan work, response size, and freshness trade-offs are not measured. The bounded exercise/health/marathon spike and activated grep measurements are complete. |
| GAP-OBS-006 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | `links`, the scoped request-local path catalog, and outbound `traverse` are not implemented. |
| GAP-OBS-007 | spike | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | The full-vault activation gate for live request-local `backlinks` and `path_between` has not been run or passed; the tools are neither implemented nor advertised. |
| GAP-OBS-008 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Descriptor-owned safe telemetry summaries are complete for the accepted five-tool core surface; summaries for links, traversal, backlinks, and path discovery are not implemented. |
| GAP-OBS-009 | proof | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | The accepted five-tool summaries have local JSONL/SQLite proof plus live model-driven `ls`, `grep`, and continued `read_many` telemetry; graph-tool summaries have not been proven. |
| GAP-OBS-010 | implementation | `docs/obsidian.md` / `docs/requirements/obsidian-filesystem-tools.md` | Live request-local `backlinks` and `path_between` are not implemented for pre-activation benchmark and proof. |
| GAP-OBS-013 | remaining formats | `docs/requirements/obsidian-document-reading.md` / `docs/spikes/issue-4-native-document-transfer.md` | PDF is activated under the 7,000,000-byte raw ceiling after local byte/MIME, capacity, resource, cleanup, and telemetry gates plus authenticated small text/visual, scanned visual/OCR, and exact-ceiling tunnel/ChatGPT journeys. Text/code, rich documents, presentations, and spreadsheets remain unsupported and keep Issue #4 open until each family is activated or explicitly dispositioned. |
