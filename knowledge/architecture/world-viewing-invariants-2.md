---
type: Architecture
title: "Map viewing â€” Invariants (part 2)"
description: "(no host allocation/publication), read-only /app/world bind of the canonical"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/worldview.proto, internal/worldview]
sources:
  - title: Parent concept
    resource: world-viewing.md
---
  (no host allocation/publication), read-only `/app/world` bind of the canonical
  level folder, pinned image/command, and deterministic per-dimension map config
  including consent. Client port/volume/command/init/URL overrides are discarded.
  Config is staged then committed after the module row persists; commit failure
  deletes the row under the still-held lock with a fresh bounded cleanup context.
  No-clobber promotion preserves a target created between staging and commit.
  The private web port uses
  a `-1` host-port sentinel skipped by every allocation/publish/reconcile path.
