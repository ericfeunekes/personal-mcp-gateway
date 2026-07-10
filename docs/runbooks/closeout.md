---
title: "Closeout Runbook"
status: draft
purpose: "List checks before declaring work complete."
covers:
  - docs/
  - cmd/
  - internal/
---

# Closeout Runbook

Before declaring work complete:

1. Confirm the current task's requirements and domain docs were read.
2. Run the proof required by `docs/TESTING.md`, or state exactly what could not be run.
3. Verify no secrets, local vault paths, tunnel credentials, exported personal data, or note contents were added.
4. Update affected docs when behavior, tool exposure, auth, access control, or tunnel assumptions changed.
5. Update `docs/feature-gap-map.md` for behavior not implemented or not proven.
6. Verify docs discoverability:
   - root `AGENTS.md` points to the relevant docs;
   - `docs/AGENTS.md` lists standard and domain docs;
   - `docs/ARCHITECTURE.md` routes to domain docs;
   - root `CLAUDE.md` and `docs/CLAUDE.md` symlinks resolve when present.
7. Report proof, residual risk, and open gaps in the final status.
