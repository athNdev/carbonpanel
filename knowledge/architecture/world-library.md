---
type: Architecture
title: World Library
description: Library worlds are immutable content-addressed revisions; servers get private writable clones through journaled, stop-based bindings, never a live folder rename.
status: stable
owners: [oss-runtime]
code_refs: [internal/worldlib, internal/db/world_library.go, proto/carbonpanel/v1/world.proto, internal/rpc/services/world.go, web/carbon-panel/src/routes/library, web/carbon-panel/src/lib/features/world-library, web/carbon-panel/src/lib/features/server-world]
sources:
  - title: World Library identity and clone-on-bind
    resource: ../../docs/adr/0016-world-library-identity-and-binding.md
---

# Parts

The library also displays running server worlds as read-only references. A
server-scoped `ListBindings` supplies the configured level. Only a binding for
that exact server and level identifies its revision. Listing creates no library
entry and leaves the save and its bindings untouched.
An explicit fleet read denial hides cached running worlds and stops their
binding reads until a successful fleet response confirms access again.

Staged family discovery treats a missing direct `level.dat` as an ordinary
non-world folder, but returns any other marker stat failure as a storage
error. The scan runs before the binding swap or any live rename, so a node
storage fault cannot publish a partial set, mutate a world, or be reported as
an unsupported archive layout.

- [World Library — Shape + Rules](world-library-shape.md) — A WorldEntry is a logical library identity (wld_*). A WorldRevision is an
- [World Library — Read next](world-library-read-next.md) — Fail-closed authorization
