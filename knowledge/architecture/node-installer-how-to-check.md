---
type: Architecture
title: "Portable node installer — How to check + Read next"
description: "go test ./internal/nodeinstall/... and"
status: stable
owners: [oss-runtime]
code_refs: [internal/nodeinstall, internal/noderuntime/enroll, cmd/carbon-node-installer, scripts/install-node.sh, internal/rpc/enrollment_integration_test.go, docs/node-installer.md]
sources:
  - title: Parent concept
    resource: node-installer.md
---
# How to check

- `go test ./internal/nodeinstall/...` and
  `python scripts/test_node_installer_fixture.py` (offline, synthetic keys only).
- Confirm no exported function writes outside its supplied directories and no
  generated metadata contains the secret, private key or credential.
- Confirm `render-daemon` only prints strings, never writes a unit or starts a
  service, and that its fixtures assert the one-source-of-options, per-user
  rootless and explicit-rootful-capability rules.
- The owned Linux systemd fixture (`scripts/qualify-node-installer-systemd.sh`)
  records real start/restart, XDG reset, canonical roots and temporary-path
  refusals. It is fixture-only: a no-op daemon stands in for dockerd and no
  full OS reboot or live host provisioning is claimed.

# Read next

- [Node control protocol](node-control-protocol.md)
- [Deployment modes](../operations/deployment-modes.md)
- [Portable node installer](../../docs/node-installer.md)
