---
type: Architecture
title: Portable node installer
description: One guest-side Linux installer shared by the host-native, VM and LXC managed modes; it owns only caller-named directories and produces ADR 0019 enrollment material.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/nodeinstall
  - internal/noderuntime/enroll
  - cmd/carbon-node-installer
  - scripts/install-node.sh
  - internal/rpc/enrollment_integration_test.go
  - docs/node-installer.md
sources:
  - title: Portable Linux node installer
    resource: ../../docs/node-installer.md
  - title: ADR 0019 node control contract
    resource: ../../docs/adr/0019-node-control-contract.md
---

# Rule

The portable installer in `internal/nodeinstall` and `cmd/carbon-node-installer`
is the **single** guest-side install path for every managed Carbon Panel mode
(host-native private daemon, QCOW2 VM, unprivileged LXC). No mode lane forks or
duplicates it; each coordinates through its exported Go functions or the CLI.
It is also the supported fallback wherever an image format is unqualified.

- It mutates only directories named by the caller (state, data, artifact,
  backup). It never edits global host config, starts/stops services, touches a
  host Docker socket or downloads an artifact. Provisioning metadata contains
  no secret: `S` stays in a `0600` state file and appears only in one claim.
- First boot creates one Ed25519 identity and UUID node ID; a repeat in the same
  state dir is a no-op, a different state dir is a different node, and a
  key/identity mismatch is an integrity failure, never key regeneration.
- Enrollment material is the **shared** ADR 0019 contract (generated proto
  messages), not a parallel DTO: a scoped secret-free bundle, a `CSR_PRE`
  registration artifact, and a one-time challenge-bound claim signed over the
  canonical context. `ClaimContext` only normalises capability names to proto
  enum names and delegates to the single shared `ClaimContextRaw` encoder; there
  is no mode-local encoder or authority. An owned integration fixture verifies
  real installer claims at the shared boundary. No enrollment handler is mounted
  by this installer and no live trust boundary or deployment is claimed.
- Artifacts are verified (platform, per-file SHA-256, optional Ed25519
  signature) before promotion. Two slots plus an atomic state file give
  repeatable upgrade/rollback; an interrupted promotion completes from a
  journal or returns `ErrUnresolvedRecovery`, never guessed success. Persistent
  data is not an input to upgrade and is never deleted by recovery.
- Private-daemon rendering is in-memory, read-only and disposable: daemon
  options are defined once in `daemon.json`, the unit carries only
  `--config-file` (no duplicated flags), rootless uses a per-user launcher and
  rootful an explicit root service with a non-empty capability bound. The
  canonical source is `internal/noderuntime/native`; the CLI wires it with
  `SetDaemonRenderer(CanonicalRenderer{})` and the installer declares no dockerd
  option locally (no invalid `bridge` + `bip` combination).
- Offline Go and Python fixtures prove refusal, state transitions and data
  preservation only. No live host import/boot, hypervisor/LXC or production
  enrollment success is claimed.

# Why

A per-mode installer would fork the enrollment contract and drift on identity,
secret handling and data safety. One scoped installer keeps those invariants
shared and lets an unqualified image fall back to a known-guest path.

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
