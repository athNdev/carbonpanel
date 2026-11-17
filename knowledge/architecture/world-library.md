---
type: Architecture
title: World Library
description: Library worlds are immutable content-addressed revisions; servers get private writable clones through journaled, stop-based bindings, never a live folder rename.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/worldlib
  - internal/db/world_library.go
  - proto/carbonpanel/v1/world.proto
  - internal/rpc/services/world.go
  - web/carbon-panel/src/routes/library
  - web/carbon-panel/src/lib/features/world-library
sources:
  - title: World Library identity and clone-on-bind
    resource: ../../docs/adr/0014-world-library-identity-and-binding.md
---

# Shape

A `WorldEntry` is a logical library identity (`wld_*`). A `WorldRevision` is an
immutable, content-addressed snapshot (SHA-256, byte size, entry count, manifest,
provenance and license attestation). A `WorldBinding` maps one
`(server_id, level_name)` to one revision; a `WorldImportJob` records durable
one-shot import state. All four are control-plane SQLite metadata; world bytes
live only on owning node storage.

# Rules

- Revisions are immutable. No procedure writes into revision bytes; an edit
  produces a new revision. A bound server writes only to its private clone.
- Two writable bindings to one revision are refused with a typed error,
  enforced by the binding store and a keyed lock held across the operation.
  A shared reference is read-only metadata and is never mounted writable.
- A live world change is a journalled stop → quiesce → node-side stage/verify →
  atomic level-family publish → rebind → restart → verify transaction with
  `request_id` idempotency and an operator-resolution path for unknown outcomes.
  A folder rename is not a hot swap. Never auto-retry an uncertain outcome.
- Node resolution is strict and fail-closed: remote-without-transport returns
  `ErrUnavailable`; there is no controller-local fallback. Work that outlives
  the request uses an owned context with cancel-and-wait on shutdown; mutation
  finalisation uses a bounded independent context.
- Compatibility fields are `detected`, `inferred` or `unknown` with reasons.
  Unknown gates the operation. Unsupported transforms (edition conversion,
  in-place downgrade, arbitrary mod removal, live NBT edits) are disabled with
  reasons.
- Imports verify platform hash, archive manifest and whole-archive SHA-256 in
  node-local staging before publication. Extraction rejects traversal, links,
  nested bombs, oversized entries and executable/plugin payloads without
  changing container privileges.
- Platform support has two axes: API-backed artifact support (Modrinth,
  CurseForge content artifacts) and experimental/manual prebuilt-world import.
  A modpack or datapack is not a prebuilt world. CurseForge `x-api-key` goes
  only to the authorized API origin and is stripped on cross-origin redirects.
- Admission and keyed locks are single-controller/process-local; no
  cross-process atomic reservation is claimed. `world_quota_mb` is advisory
  admission, never `WorldDiskEnforced`.

# Read next

- [World Library ADR](../../docs/adr/0014-world-library-identity-and-binding.md)
- [Operation journal](operation-journal.md)
- [Node resolution](node-resolution.md)
- [Data safety](../invariants/data-safety.md)
- [Fail-closed authorization](../invariants/authz-fail-closed.md)
