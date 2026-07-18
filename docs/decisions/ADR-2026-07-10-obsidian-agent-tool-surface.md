---
title: "ADR 2026-07-10 Obsidian Agent Tool Surface"
status: accepted
purpose: "Record the grep-first, composable read and authored-reference tool vocabulary for the Obsidian MCP server."
covers:
  - internal/tools/obsidian/
  - internal/fsx/
  - docs/obsidian.md
  - docs/requirements/obsidian-filesystem-tools.md
---

# ADR 2026-07-10: Obsidian Agent Tool Surface

## Status

Accepted. This ADR supersedes the example tool vocabulary in
`ADR-2026-07-02-obsidian-server-tool-names.md` and only the Obsidian
`search`/`grep` split in `ADR-2026-06-30-mcp-sdk-tunnel-runtime.md`. Their
server naming, SDK, tunnel, runtime, and supervision decisions remain accepted.

## Decision

The `obsidian` MCP server targets these simple read-only tool names:

- `resolve`
- `ls`
- `read`
- `read_many`
- `grep`
- `links`
- `traverse`
- `backlinks`
- `path_between`

Use `grep` as the familiar universal content-discovery operation. Do not expose
a separate `search`, `stat`, or overloaded `graph_search`: `resolve` already
owns path metadata, and explicit graph verbs make direction, scope, cost, and
completeness easier for an agent to control.

`links` and shallow outbound `traverse` are the graph foundation. `links`
performs only local/direct resolution; traversal uses a bounded request-local
path catalog and does not resolve aliases. `backlinks` and `path_between`
remain first-class target tools, but their live request-local implementation
and activation are gated on full-vault performance, freshness, response-size,
and machine-impact proof. No persistent/background index is selected by this
decision.

All scans and graph operations report result completeness separately from
declared-scope completeness and expose bounded work. Negative graph answers are
conclusive only when the declared scope was completely examined.

## Evidence

A bounded spike used 47 exercise, health, and marathon notes totaling about
231 KiB. Direct grep completed in roughly 12-41 ms. After content loading,
request-local graph construction took roughly 1-3 ms and graph operations were
sub-millisecond. The workflows showed:

- sparse workout notes depended on `grep` plus `read_many` rather than graph expansion;
- an outbound depth-two walk recovered a useful nine-note authored evidence chain;
- bidirectional expansion grew that same seed set to 24 notes, useful in some cases but too broad as a default;
- full-scope negative answers and inbound work could not be justified from the bounded corpus.

These are requirements-shaping observations, not implementation benchmarks.
Detailed schemas, resource limits, acceptance criteria, and open proof gates
live in `docs/requirements/obsidian-filesystem-tools.md`.

## Consequences

- Agent workflows compose `grep`, bounded reads, and explicit reference operations instead of relying on one ranked search surface.
- Obsidian-specific parsing and reference resolution remain in the Obsidian domain, not the generic filesystem adapter.
- Inbound and path discovery cannot silently choose a live scan, cache, or persistent index during implementation planning.
- Tool-list changes require schema, vault-safety, telemetry, model-selection, and live connector proof.
