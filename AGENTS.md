# Personal MCP Gateway Notes

This project exposes personal data surfaces. Treat security boundaries as product behavior, not infrastructure detail.

## Project Overview

- Go gateway source belongs under `cmd/gateway/` and `internal/`.
- Gateway architecture and boundaries live in `docs/ARCHITECTURE.md`.
- Obsidian server behavior lives in `docs/obsidian.md`.
- First implementation requirements live in `docs/requirements/obsidian-filesystem-tools.md`.
- Open gaps are tracked in `docs/feature-gap-map.md`.

## Workflow

- Before changing architecture, tool exposure, or MCP server boundaries, read `docs/ARCHITECTURE.md` and the affected domain doc.
- Before implementing the Obsidian server, read `docs/obsidian.md` and `docs/requirements/obsidian-filesystem-tools.md`.
- Before using or updating OpenAI product assumptions, use the repo-local `openaiDeveloperDocs` MCP server from `.codex/config.toml`; if the tools are not exposed in the running session, restart Codex or use official OpenAI docs as a bounded fallback.
- Before declaring work done, follow `docs/runbooks/closeout.md`.

## Invariants

- Default to read-only tools unless the user explicitly asks for writes.
- Do not add broad filesystem, shell, or generic HTTP proxy tools.
- Keep integrations narrow and named: YNAB, Obsidian, Voicenotes, or other explicit systems.
- Do not commit secrets, tokens, tunnel credentials, local database paths, or exported personal data.
- Prefer explicit capability allowlists over deny lists.
- When changing auth, access control, or tool exposure, document the observable implication in the README or a design note.
- No legacy or fallback paths unless explicitly requested.
- Use the MCP server name for integration ownership. The Obsidian MCP server is named `obsidian` and exposes simple tool names such as `ls`.
- Keep filesystem-like tools stateless: pass explicit `path`, `base`, limits, and cursors instead of relying on server-side current-directory state.
- Reliability, bounded resource use, and minimal machine impact outrank broader vault traversal features.

## Build And Verify

Canonical local proof is documented in `docs/TESTING.md`. Run `make test` before declaring implementation changes complete.
