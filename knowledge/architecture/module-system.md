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
  - internal/blueprint/resolve.go
  - internal/module/hooks.go
  - internal/module/reconcile.go
  - internal/docker/client.go
  - internal/reconciler
  - internal/rpc/services/server.go
  - web/carbon-panel/src/lib/module-identity.ts
  - web/carbon-panel/src/routes/modules/ActiveModules.svelte
  - web/carbon-panel/src/lib/components/server/ServerModules.svelte
  - web/carbon-panel/src/routes/servers/new/+page.svelte
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
- Provider credentials must not accompany keyless fallbacks or cross-origin
  redirects. The CurseForge adapter owns fallback policy; see
  [provider credentials](../decisions/provider-http-policy.md).
- A hook failure must be attributable to the module and the server.
- Create-path blueprints are Java/tag seeds resolved to image, env and
  protocol. A Bedrock full-image seed is labeled unsupported and is not a
  native edition path. See [blueprint create qualification](../decisions/blueprint-create-qualification.md).

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

An external replace or reconnect is adopted from the live labeled container
(never a retained `carbon-panel-replaced-*` name, never an exited sibling).
Identity, status and recorded host ports stay on that one sidecar. A missing
container under a running parent is restored without `RemoveContainer`,
because that call always sets `RemoveVolumes`. A failed start hook retries
against the observed container and does not create a second one. A parent
row that is already gone marks the child stopped and does not resurrect it.
The Modules tab renders container id and status from that reconciled
`listModules` record (`moduleIdentityFromRecord`), not from a stale inspect
helper.

Privileged host-extension templates (Docker socket, `privileged: true`) are
operator features. Builtin templates do not ship them, `CreateModuleContainer`
clears `HostConfig.Privileged`, and bind sources stay inside the server data
path and backup dir so a socket mount is not auto-broadened.

Parent deletion has an explicit child policy. A full delete stops and
removes sidecar containers before the server row. `DeleteServer` with
`keep_data` drops module panel records through `ForgetModule` and leaves
the containers in place, because `RemoveContainer` always sets
`RemoveVolumes`. Detach skips child cleanup so sidecars keep running with
the detached server. After a parent migration, module rows follow the
target node and clear their container ids; source sidecar containers are
not removed.

# Hook execution

EXEC hooks resolve the module's `NodeID` through the Docker Locator with the
operation context before executing on its recorded container. An unresolved or
unavailable assigned node returns an error without executing locally. A remote
pool client works even when no controller-local client is configured.

The command remains one exec argument; routing adds no shell or command splitting.
An absent container remains a no-op. Cancellation already present on entry stops
the hook before lookup, and the operation context reaches the Docker exec calls.
The event dispatcher retains its bounded hook slots and existing event-context
lifetime policy. Tests use two loopback fake daemons to prove remote execution,
local non-interference, command/exit behavior and cancellation.

# Read next

- [Node resolution](node-resolution.md)
- [Known failure modes](../failures/remote-node-fallback.md)
