---
type: Architecture
title: Node resolution
description: Every operation must resolve the Docker client for the target node and fail closed when it cannot.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/docker
  - internal/docker/locator.go
  - internal/module
  - internal/snapshot
  - internal/docker/freezer.go
  - internal/metrics
  - internal/nodestorage
sources:
  - title: Fail-closed node resolution change
    resource: https://github.com/athNdev/carbon-panel/pull/225
---

# Rule

A server belongs to a node. An operation that targets a remote node must obtain
that node's Docker client or return an error. It must never fall back to the
controller's local Docker socket.

# Why

A silent fallback can run a module, snapshot or sleep operation against the
wrong host. That is a data-safety bug, not a degraded mode.

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
  generate the server) is written through `nodestorage.ForNode`. A remote node
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
  before the panel row. A remote node with no reachable storage is refused,
  and a controller-local directory of the same name is left in place.
- A host-port check asks the selected daemon which protocol is published. A
  UDP publish does not occupy the same number on TCP, and a TCP publish does
  not occupy it on UDP. The controller socket table is consulted only for the
  local node, and only for the protocol being reserved.
- A durable port lease is unique per node, port, protocol, and address. An
  IPv4 wildcard claim overlaps every IPv4 address of that port, and an IPv6
  wildcard overlaps every IPv6 address. The other family stays free. Additional
  binds are claimed before the server row is kept, and a failed create drops
  those claims. Moving a server moves its leases onto the target node, and
  leaves them on the source when the target already holds that bind.
- DOCKER-USER rate limiting and proxy isolation report that enforcement is
  unavailable when it was requested and iptables cannot run. The container is
  still created. A disabled firewall is not described as applied.
- Network modes `host`, `none`, and `container:<name>` are refused. They do
  not publish the host ports recorded by the lease. Bridge and named networks
  still publish that address, protocol, and port. A failed create keeps the
  requested port.

# How to check

For any new operation, follow the path from the server record to the Docker
client. Add a test where the node cannot be resolved and assert an error.

# Read next

- [Failure modes](../failures/remote-node-fallback.md)
- [Module system](module-system.md)
