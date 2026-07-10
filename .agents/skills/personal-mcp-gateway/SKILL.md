---
name: personal-mcp-gateway
description: Use when working in this repo on the personal MCP gateway, Obsidian MCP server, OpenAI Secure MCP Tunnel setup, simple tool names such as ls/resolve inside the obsidian server, or reliability and machine-impact requirements for local personal-data tools.
---

# Personal MCP Gateway

Use this skill to ground work in the repo's current gateway decisions before editing code or docs.

## Start Here

1. Read `AGENTS.md`.
2. Read `docs/ARCHITECTURE.md`.
3. For Obsidian work, read `docs/obsidian.md` and `docs/requirements/obsidian-filesystem-tools.md`.
4. Check `docs/feature-gap-map.md` for open implementation, proof, decision, and spike gaps.

## Current Architecture Contract

- Implement the gateway core in Go.
- Use one local gateway process until there is concrete pressure to split.
- Use the MCP server name as the public namespace contract. The Obsidian MCP server is named `obsidian` and exposes simple tool names such as `ls` and `resolve`.
- Keep Obsidian filesystem-like tools stateless with explicit `path`, `base`, limits, and cursors.
- Keep phase 1 read-only.
- Use OpenAI Secure MCP Tunnel, not Cloudflare.
- Use the repo-local OpenAI docs MCP config in `.codex/config.toml`; restart Codex from this repo if `openaiDeveloperDocs` is not exposed in the current session.

## Guardrails

- Do not add generic filesystem, shell, or HTTP proxy tools.
- Do not commit secrets, vault paths, exported personal data, tunnel credentials, local database paths, or note contents.
- Do not add writes or background indexers without a new requirement and proof plan.
- Treat reliability, bounded resource use, and minimal machine impact as product behavior.

## Closeout

Follow `docs/runbooks/closeout.md` before reporting work complete.
