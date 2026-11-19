---
type: Architecture
title: "Portable node installer — How to check + Read next"
description: "go test ./internal/nodeinstall/... and"
status: stable
owners: [oss-runtime]
code_refs: [internal/nodeinstall, internal/noderuntime/enroll, cmd/carbon-node-installer, scripts/install-node.sh, scripts/qualify-node-installer-systemd.sh, internal/rpc/enrollment_integration_test.go, docs/node-installer.md]
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
  records installer selection/rendering, real start/restart, XDG reset,
  canonical roots and temporary-path refusals, authenticated enrollment
  acceptance at the shared production boundary seam (disposable synthetic-key
  fixtures), and upgrade/rollback for native rootless/rootful and captive
  rootless profiles, with explicit evidence levels. Captive rootful is recorded
  as unsupported for unprivileged LXC captive deployment by design. Fixture-only:
  no-op daemon, no live host/guest boot, no nested captive-docker runtime, no
  shipped-unit boot with real dockerd, no OS reboot, and no production enrollment
  or deployment.

# Read next

- [Node control protocol](node-control-protocol.md)
- [Deployment modes](../operations/deployment-modes.md)
- [Portable node installer](../../docs/node-installer.md)
