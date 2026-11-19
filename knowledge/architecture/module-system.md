---
type: Architecture
title: Module system
description: Modules, hooks, packwiz and indexers deliver content. Module reconcile adopts an externally replaced sidecar without duplicating it; EXEC hooks use the manager's Docker client for the target node.
status: stable
owners: [oss-runtime]
code_refs: [internal/module, internal/packwiz, internal/indexers, internal/blueprint, internal/blueprint/resolve.go, internal/module/hooks.go, internal/module/reconcile.go, internal/docker/client.go, internal/reconciler, internal/rpc/services/server.go, web/carbon-panel/src/lib/module-identity.ts, web/carbon-panel/src/routes/modules/ActiveModules.svelte, web/carbon-panel/src/lib/components/server-ui/ServerModules.svelte, web/carbon-panel/src/routes/servers/new/+page.svelte]
sources:
  - title: Module delivery checksum issue
    resource: ../../docs/issues/003-MOD-4-modpack-delivery-checksum.md
  - title: Indexer error surfacing issue
    resource: ../../docs/issues/004-MOD-2-surface-indexer-errors.md
---

# Parts

- [Module system — Shape + Rules](module-system-shape.md) — Modules are server-side content units. Hooks run inside the server's runtime;
- [Module system — Convergence](module-system-convergence.md) — Module containers are reconciled by the same level-triggered engine as server
- [Module system — Convergence (part 2)](module-system-convergence-2.md) — Parent deletion has an explicit child policy. A full delete stops and
- [Module system — Hook execution + Read next](module-system-hook-execution.md) — EXEC hooks resolve the module's NodeID through the Docker Locator with the

# Key facts

- Every module operation must use the Docker client resolved for the target
- The decision itself is a pure function in `internal/module/reconcile.go`; the
- Privileged host-extension templates (Docker socket, `privileged: true`) are
- A hook failure must be attributable to the module and the server.
- covers every module that has a container. The pass adopts a container id or
- stopped, and recreates a missing container only when both the module and its
- with the module manager. When a pass adopts, recreates, or records a stopped
- module on that server is scheduled. The sweep still revisits every running
- module row even when its container id is empty.
- Identity, status and recorded host ports stay on that one sidecar. A missing
- the detached server. After a parent migration, module rows follow the
- EXEC hooks resolve the module's `NodeID` through the Docker Locator with the
- Module containers are reconciled by the same level-triggered engine as server
- containers. A module event schedules a module pass, and the periodic sweep also
- self-heal recreate that records `starting`), a running module with no
- While a declared dependency is not running, the pass neither reports the module
- `Module.Ports` using the same store and daemon reservation checks as module
- module, the reconciler also schedules any module that declared a dependency on
- it, so a waiting sidecar can converge without waiting only on the periodic
- The Modules tab renders container id and status from that reconciled
- `keep_data` drops module panel records through `ForgetModule` and leaves
- target node and clear their container ids; source sidecar containers are
- server. Do not use the manager's client directly.
- parent server are meant to be running. A sidecar is never resurrected under a
- stopped server. While the parent is still coming up (for example after a
- container under a running parent is restored without `RemoveContainer`,
- removes sidecar containers before the server row. `DeleteServer` with
- the hook before lookup, and the operation context reaches the Docker exec calls.
- Modules are server-side content units. Hooks run inside the server's runtime;
- status the daemon disagrees with, records a container that disappeared as
- container waits and keeps its row; marking it gone would prevent recreate
- because a daemon that did not answer is not evidence the container is gone.
- unchanged. Before recreate it re-checks each published host port on
- reconciler only schedules it, so templates, hooks and dependency ordering stay
- adopts or recreates its container, or transitions to running or stopped, every
- An external replace or reconnect is adopted from the live labeled container
