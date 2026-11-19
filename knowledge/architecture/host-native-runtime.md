---
type: Architecture
title: Host-native private daemon mode
description: Mode-specific Linux runtime that owns a private Docker socket, roots and network; it consumes the shared node-control contract and never falls back to a host engine.
status: stable
owners: [oss-runtime]
code_refs: [internal/noderuntime/native, cmd/carbon-node, docs/plans/host-native-private-daemon.md]
sources:
  - title: ADR 0019 node control contract
    resource: ../../docs/adr/0019-node-control-contract.md
  - title: Host-native mode prerequisite and isolation contract
    resource: ../../docs/plans/host-native-private-daemon.md
---

# Parts

- [Host-native private daemon mode — Rule](host-native-runtime-rule.md) — internal/noderuntime/native is the HOST-NATIVE LINUX PRIVATE DAEMON mode for
- [Host-native private daemon mode — Rule (part 2)](host-native-runtime-rule-2.md) — issue-certificate-first atomic consume, controller-derived TLS topology
- [Host-native private daemon mode — Why + How to check + Read next](host-native-runtime-why.md) — Parallel deployment modes share one enrollment/control contract. Keeping host
