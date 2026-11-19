---
type: Architecture
title: "Node resolution — Current state"
description: "Module, snapshot and deep-sleep helpers were made fail-closed and record the"
status: stable
owners: [oss-runtime]
code_refs: [internal/docker, internal/docker/locator.go, internal/module, internal/snapshot, internal/docker/freezer.go, internal/metrics, internal/nodestorage]
sources:
  - title: Parent concept
    resource: node-resolution.md
---
# Current state

- Module, snapshot and deep-sleep helpers were made fail-closed and record the
  server's node ID.
- A cached Docker client is dropped when that node's host, TLS material, or
  endpoint generation changes. The next lookup builds a client for the current
  endpoint. A client registered without a recorded endpoint is left in place.
- Creating a node starts its endpoint generation at 1. Changing the host or
  TLS material increases that generation. A lifecycle operation records the
  generation and the accepted Docker daemon id it observed.
- The first successful daemon observation stores that daemon id. A later
  observation with a different id is refused, the cached client is dropped,
  and the accepted id stays in place until an operator update replaces it.
- `GetClient` does not serve a named node from the local daemon, including
  when the historical fallback flag is set. Local aliases still use the
  controller client.
- `GetClientStrictContext` carries operation cancellation through node lookup,
  cached endpoint validation and eager API-version negotiation. Canceled cold
  construction closes its client instead of caching it or starting network
  setup. Legacy wrappers keep their bounded background lifetime.
  Missing-network create/start retries use the same operation context and
  retain both the original daemon failure and a failed network repair.
  Locator client/executor resolution forwards that context and preserves
  cancellation before considering any local alias or fallback.
- Hibernation uses `SetFreezerResolver` so freeze/wake go through `internal/docker/locator.go` for the server's node. The remaining freezer exposure is a resolver that returns nil rather than a typed locator error.
- Module EXEC hooks resolve the module's assigned node through Locator with
  the operation context. Lookup and remote execution failures never retry on
  the manager's local daemon; no local client is required for a remote hook.
- Disk usage measures the node's mapped world root. A remote node without
  reachable storage is left unmeasured.
- File read, update, delete, and rename resolve that mapped root before
  checking the path. A controller copy of the data path is not consulted.
  The latest-log upload uses that same root.
- Creating a server writes the world directory on that mapped root. A manual
  server pack (the zip the panel extracts onto the world so the image can
  generate the server) is written through the instance-owned resolver. A remote node
  without a reachable mount or remote adapter is refused, so no
  controller-local world or zip is created.
- Scheduled backup archives resolve the mapped root before reading the world.
- Mod installation and packwiz bake write the mods folder on that mapped
  root. A remote node with no reachable storage is refused, and a controller
  copy is not treated as the installed set.
- Snapshot create and restore archive that mapped root. A remote node with
  no reachable storage is refused before a controller copy is archived.
- Scheduled modpack sync clones and copies onto that mapped root. A remote
  node with no reachable storage is refused before a controller directory
  is created.
- Deleting a server removes the world on the node's mapped storage root
