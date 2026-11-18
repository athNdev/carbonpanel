---
type: Architecture
title: Host-native private daemon mode
description: Mode-specific Linux runtime that owns a private Docker socket, roots and network; it consumes the shared node-control contract and never falls back to a host engine.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/noderuntime/native
  - cmd/carbon-node
  - docs/plans/host-native-private-daemon.md
sources:
  - title: ADR 0019 node control contract
    resource: ../../docs/adr/0019-node-control-contract.md
  - title: Host-native mode prerequisite and isolation contract
    resource: ../../docs/plans/host-native-private-daemon.md
---

# Rule

`internal/noderuntime/native` is the HOST-NATIVE LINUX PRIVATE DAEMON mode for
managed nodes. It is mode-specific and consumes the shared node-control
contract in `proto/carbonpanel/v1/node_control.proto`; it does not add proto
fields, RPCs, shared DB columns or auth middleware.

- A dedicated daemon process owns a private Unix socket, private data and exec
  roots, a dedicated containerd namespace and a private bridge / port range.
  Rendering refuses any spec with the host socket, a shared root/namespace, a
  public raw API listener or blanket privileges.
- Compatibility gates select rootless when user namespaces, subuid/subgid,
  delegated cgroup v2 controllers, a qualified storage driver and a private
  0700 runtime dir are all present. Otherwise a hardened dedicated rootful
  service is the explicit fallback. Both paths require the universal isolation
  gates; an unqualified host fails closed with a precise refusal code.
- A preinstalled/shared Docker daemon is never a fallback. Its presence is
  reported but never used to qualify the mode or route an operation.
- The enrollment boundary implements the ADR 0019 scheme against shared proto
  types: node-generated secret, REQUIRED approved key, `CSR_PRE` as material,
  challenge-bound fresh `SIG_CLAIM`, single-use atomic consume, controller-
  derived TLS topology support/refusal, a signed public client certificate and
  three non-crossing authorization domains. No secret is stored or returned in
  a read response.
- `cmd/carbon-node` is the bounded preflight/doctor runtime caller. It reads an
  injected HostFacts document; it never probes or changes a live host, installs
  a daemon, writes a host unit or mounts a real socket. Fixtures use synthetic
  keys and temp-dir private material only.

# Why

Parallel deployment modes share one enrollment/control contract. Keeping host
prerequisites, private-isolation gates and the local secret store in a
mode-specific package avoids forking the contract while making every
privilege decision explicit and testable without a live host.

# How to check

- `go test ./internal/noderuntime/... ./cmd/carbon-node` and `go vet` on the
  touched packages.
- The gate matrix and refusal codes are in
  `docs/plans/host-native-private-daemon.md`.
- Fixtures prove refusal/state-machine outcomes only. No live-host, install,
  gameplay or deployment success may be claimed from them.

# Read next

- [Node control protocol](node-control-protocol.md)
- [ADR 0019](../../docs/adr/0019-node-control-contract.md)
