---
title: "Workflow"
status: draft
purpose: "Define how work progresses from requirements to verified local MCP behavior."
covers:
  - docs/
  - cmd/
  - internal/
---

# Workflow

## Normal Flow

1. **Requirements**: capture the user-visible behavior and non-goals in `docs/requirements/`.
2. **Architecture fit**: update `docs/ARCHITECTURE.md` or the owning domain doc when work changes language, process topology, MCP server boundaries, state, auth, or tunnel behavior.
3. **Implementation**: keep Go code inside the planned module boundaries; do not add a generic filesystem, shell, or HTTP proxy surface.
4. **Proof**: run the proof mapped in `docs/TESTING.md`, including reliability and machine-impact checks when a change touches filesystem traversal, search, tunnel behavior, or process lifecycle.
5. **Closeout**: follow `docs/runbooks/closeout.md` and update `docs/feature-gap-map.md` for anything not implemented or not proven.

## Work Routing

- New MCP server or tool family: update `docs/ARCHITECTURE.md`, add or update the domain doc, then add a requirement under `docs/requirements/`.
- Obsidian tool behavior: read `docs/obsidian.md` and `docs/requirements/obsidian-filesystem-tools.md`.
- OpenAI docs or tunnel assumptions: use the repo-local `openaiDeveloperDocs` MCP config in `.codex/config.toml`; verify current docs before implementation.
- Reliability or resource limits: update `docs/TESTING.md` and the owning domain doc before encoding behavior.

## Context Recovery

After context loss, interruption, or a long break:

1. Re-read root `AGENTS.md`.
2. Re-read `docs/ARCHITECTURE.md`.
3. Re-read the relevant domain and requirements docs.
4. Check `docs/feature-gap-map.md` before deciding what is done.
