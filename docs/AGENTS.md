---
title: "Docs Navigator"
status: draft
purpose: "Route future agents to the durable planning, architecture, testing, and runbook surfaces for this repo."
covers:
  - docs/
---

# Docs Navigator

Read this file first when navigating `docs/`.

## Standard Surfaces

- `ARCHITECTURE.md` — gateway topology, module boundaries, MCP server naming strategy, and implementation language.
- `WORKFLOW.md` — how planning, implementation, proof, and closeout move through this repo.
- `TESTING.md` — proof contract for MCP behavior, vault safety, reliability, and machine impact.
- `RUNBOOKS.md` — index of operational procedures.
- `runbooks/closeout.md` — checks before declaring work complete.
- `runbooks/openai-tunnel.md` — foreground tunnel setup and local secret placement.

## Domain Docs

- `gateway.md` — local gateway process, OpenAI tunnel boundary, lifecycle, config, and observability.
- `obsidian.md` — Obsidian MCP server contract and filesystem-like read tools.

## Requirements

- `requirements/obsidian-filesystem-tools.md` — first implementation slice for `obsidian` read-only tools.
- `feature-gap-map.md` — open implementation, proof, decision, and spike gaps.

## Decisions

- `decisions/ADR-2026-06-30-go-dotted-tool-names.md` — superseded language and dotted-name decision.
- `decisions/ADR-2026-07-02-obsidian-server-tool-names.md` — current server-name namespace decision.
- `decisions/ADR-2026-06-30-mcp-sdk-tunnel-runtime.md` — MCP SDK, tunnel adapter, search dependency, and supervision decision.

## Adding Docs

Add domain-level behavior to the owning domain doc. Add individual capability requirements under `docs/requirements/` and link them from the owning domain plus `docs/ARCHITECTURE.md` when they affect boundaries.
