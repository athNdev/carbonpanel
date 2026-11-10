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
  - internal/rpc/services/server.go
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
- Verify checksums before declaring a modpack delivered.
- A hook failure must be attributable to the module and the server.

# Convergence

Module containers are reconciled by the same level-triggered engine as server
containers. A module event schedules a module pass, and the periodic sweep also
covers every module that has a container. The pass adopts a container id or
status the daemon disagrees with, records a container that disappeared as
stopped, and recreates a missing container only when both the module and its
parent server are meant to be running. A sidecar is never resurrected under a
stopped server. While the parent is still coming up (for example after a
self-heal recreate that records `starting`), a running module with no
container waits and keeps its row; marking it gone would prevent recreate
once the parent is running. An unreachable node leaves the record untouched
because a daemon that did not answer is not evidence the container is gone.

While a declared dependency is not running, the pass neither reports the module
in sync nor recreates it; it returns a waiting outcome and leaves the row
unchanged. Before recreate it re-checks each published host port on
`Module.Ports` using the same store and daemon reservation checks as module
create, and waits instead of recreating when another consumer holds the port.

The decision itself is a pure function in `internal/module/reconcile.go`; the
reconciler only schedules it, so templates, hooks and dependency ordering stay
with the module manager. When a pass adopts, recreates, or records a stopped
module, the reconciler also schedules any module that declared a dependency on
it, so a waiting sidecar can converge without waiting only on the periodic
sweep. The same propagation runs for the parent server: when a server pass
adopts or recreates its container, or transitions to running or stopped, every
module on that server is scheduled. The sweep still revisits every running
module row even when its container id is empty.

Parent deletion has an explicit child policy. A full delete stops and
removes sidecar containers before the server row. `DeleteServer` with
`keep_data` drops module panel records through `ForgetModule` and leaves
the containers in place, because `RemoveContainer` always sets
`RemoveVolumes`. Detach skips child cleanup so sidecars keep running with
the detached server. After a parent migration, module rows follow the
target node and clear their container ids; source sidecar containers are
not removed.

# Known gap

`internal/module/hooks.go:execInModule` still uses the manager's client
directly. It is tracked with the other node-resolution gaps in
[failure modes](../failures/remote-node-fallback.md).

# Read next

- [Node resolution](node-resolution.md)
- [Known failure modes](../failures/remote-node-fallback.md)
