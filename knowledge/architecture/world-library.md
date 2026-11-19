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

- [World Library — Shape + Rules](world-library-shape.md) — A WorldEntry is a logical library identity (wld_*). A WorldRevision is an
- [World Library — Read next](world-library-read-next.md) — Fail-closed authorization
