---
type: Architecture
title: "World Library — Shape + Rules"
description: "A WorldEntry is a logical library identity (wld_*). A WorldRevision is an"
status: stable
owners: [oss-runtime]
code_refs: [internal/worldlib, internal/db/world_library.go, proto/carbonpanel/v1/world.proto, internal/rpc/services/world.go, web/carbon-panel/src/routes/library, web/carbon-panel/src/lib/features/world-library, web/carbon-panel/src/lib/features/server-world]
sources:
  - title: Parent concept
    resource: world-library.md
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
  `request_id` idempotency, an operator-resolution path for unknown outcomes,
  and an idempotent request-keyed activity event on successful bind/unbind.
  A folder rename is not a hot swap. Never auto-retry an uncertain outcome.
  Rollback data preservation is in
  [World Library — Rollback rules](world-library-shape-2.md).
- Node resolution is strict and fail-closed: remote-without-transport returns
  `ErrUnavailable`; there is no controller-local fallback. Work that outlives
  the request uses an owned context with cancel-and-wait on shutdown; mutation
  finalisation uses a bounded independent context.
- Compatibility fields are `detected`, `inferred` or `unknown` with reasons.
  Unknown gates the operation. Unsupported transforms (edition conversion,
  in-place downgrade, arbitrary mod removal, live NBT edits) are disabled with
  reasons. A read-only report is valid only for the exact checked tuple;
  selection or navigation invalidates it and confirmation re-requires it.
- The server-detail World tab reports unmanaged and unbound states without
  inventing a level, and reuses these binding gates for pick/swap. See
  [Library adoption fencing](../decisions/library-adoption-fencing.md).
- Imports verify whole-archive SHA-256; ZIP CRC is the only per-file check.
  Cancellable inspection applies safety limits and classifies markers on the
  extraction path. Import, preview and bind refuse unusable layouts.
  See [Import layout](../../docs/world-library-import-layout.md).
- A world-bind journal protects the configured level and custom-property
  override from staged configuration and global sync.
- Platform support has two axes: API-backed artifact support (Modrinth,
  CurseForge content artifacts) and experimental/manual prebuilt-world import.
  A modpack or datapack is not a prebuilt world. CurseForge `x-api-key` goes
  only to the authorized API origin and is stripped on cross-origin redirects.
- Admission and keyed locks are single-controller/process-local; no
  cross-process atomic reservation is claimed. `world_quota_mb` is advisory
  admission, never `WorldDiskEnforced`.
