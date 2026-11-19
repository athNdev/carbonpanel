---
type: Architecture
title: "Host-native private daemon mode — Why + How to check + Read next"
description: "Parallel deployment modes share one enrollment/control contract. Keeping host"
status: stable
owners: [oss-runtime]
code_refs: [internal/noderuntime/native, cmd/carbon-node, docs/plans/host-native-private-daemon.md]
sources:
  - title: Parent concept
    resource: host-native-runtime.md
---
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
- `CheckOptionParity` fixtures fail on duplicate keys, extra CLI flags and
  CLI/config conflicts; `systemd-analyze verify` only checks unit syntax.
- Disposable dind boot evidence is in `node-native-boot-evidence.md`; the exact
  rootful unit re-qualification is in `node-runtime-requalification-20261119.md`
  and the exact rootless boot in
  `node-rootless-qualification-20261119.md` (all under `security-runtime-plans/`).
  Reproducible fixture scripts are under `qualfixtures/rootless/`. Both modes
  boot on clean disposable systemd fixtures; no live-host, install, gameplay or
  deployment success may be claimed from fixtures.

# Read next

- [Shared enrollment boundary](shared-enrollment-boundary.md)
- [Node control protocol](node-control-protocol.md)
- [ADR 0019](../../docs/adr/0019-node-control-contract.md)
