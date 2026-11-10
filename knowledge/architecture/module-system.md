---
type: Architecture
title: Module system
description: Modules, hooks, packwiz and indexers deliver server content and modpacks.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/module
  - internal/packwiz
  - internal/indexers
  - internal/blueprint
  - internal/module/hooks.go
sources:
  - title: Module delivery checksum issue
    resource: ../../docs/issues/003-MOD-4-modpack-delivery-checksum.md
  - title: Indexer error surfacing issue
    resource: ../../docs/issues/004-MOD-2-surface-indexer-errors.md
---

# Shape

Modules are server-side content units. Hooks run inside the server's runtime;
packwiz and the indexers resolve CurseForge and Modrinth content; blueprints
describe a server's desired configuration.

# Rules

- Every module operation must use the Docker client resolved for the target
  server. Do not use the manager's client directly.
- Surface indexer and delivery failures; do not turn a partial download into a
  successful install.
- Exports honor options (client inclusion, optional handling, overrides); a
  CurseForge export with non-CF mods is refused with the drop list rather
  than silently shipping without them.
- Verify checksums before declaring a modpack delivered.
- A hook failure must be attributable to the module and the server.

# Convergence

Module containers are reconciled by the same level-triggered engine as server
containers. A module event schedules a module pass, and the periodic sweep also
covers every module that has a container. The pass adopts a container id or
status the daemon disagrees with, records a container that disappeared as
stopped, and recreates a missing container only when both the module and its
parent server are meant to be running. A sidecar is never resurrected under a
stopped server, and an unreachable node leaves the record untouched because a
daemon that did not answer is not evidence the container is gone.

While a declared dependency is not running, the pass neither reports the module
in sync nor recreates it; it returns a waiting outcome and leaves the row
unchanged. Before recreate it re-checks each published host port on
Module.Ports using the same store and daemon reservation checks as module
create, and waits instead of recreating when another consumer holds the port.

The decision itself is a pure function in `internal/module/reconcile.go`; the
reconciler only schedules it, so templates, hooks and dependency ordering stay
with the module manager.

# Known gap

`internal/module/hooks.go:execInModule` still uses the manager's client
directly. It is tracked with the other node-resolution gaps in
[failure modes](../failures/remote-node-fallback.md).

# Read next

- [Node resolution](node-resolution.md)
- [Known failure modes](../failures/remote-node-fallback.md)
