---
type: Architecture
title: "Node resolution — Current state (part 2)"
description: "before the panel row. A remote node with no reachable storage is refused,"
status: stable
owners: [oss-runtime]
code_refs: [internal/docker, internal/docker/locator.go, internal/module, internal/snapshot, internal/docker/freezer.go, internal/metrics, internal/nodestorage]
sources:
  - title: Parent concept
    resource: node-resolution.md
---
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
