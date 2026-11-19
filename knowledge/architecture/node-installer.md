---
type: Architecture
title: Portable node installer
description: One guest-side Linux installer shared by the host-native, VM and LXC managed modes; it owns only caller-named directories and produces ADR 0019 enrollment material.
status: stable
owners: [oss-runtime]
code_refs: [internal/nodeinstall, internal/noderuntime/enroll, cmd/carbon-node-installer, scripts/install-node.sh, internal/rpc/enrollment_integration_test.go, docs/node-installer.md]
sources:
  - title: Portable Linux node installer
    resource: ../../docs/node-installer.md
  - title: ADR 0019 node control contract
    resource: ../../docs/adr/0019-node-control-contract.md
---

# Parts

- [Portable node installer — Rule + Why](node-installer-rule.md) — The portable installer in internal/nodeinstall and cmd/carbon-node-installer
- [Portable node installer — How to check + Read next](node-installer-how-to-check.md) — go test ./internal/nodeinstall/... and
