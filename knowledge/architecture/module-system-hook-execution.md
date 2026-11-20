---
type: Architecture
title: "Module system — Hook execution + Read next"
description: "EXEC hooks resolve the module's NodeID through the Docker Locator with the"
status: stable
owners: [oss-runtime]
code_refs: [internal/module, internal/packwiz, internal/indexers, internal/blueprint, internal/blueprint/resolve.go, internal/module/hooks.go, internal/module/reconcile.go, internal/docker/client.go, internal/reconciler, internal/rpc/services/server.go, web/carbon-panel/src/lib/module-identity.ts, web/carbon-panel/src/routes/modules/ActiveModules.svelte, web/carbon-panel/src/lib/components/server-ui/ServerModules.svelte, web/carbon-panel/src/routes/servers/new/+page.svelte]
sources:
  - title: Parent concept
    resource: module-system.md
---
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
