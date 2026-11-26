---
type: Architecture
title: Managed node outbound agent
description: The managed agent binds enrollment and recurring health reports to an explicitly selected controller and private Docker daemon.
status: stable
owners: [oss-runtime]
code_refs: [internal/noderuntime/agent, cmd/carbon-node/run.go, internal/nodeinstall/enrollment.go, internal/nodeinstall/credential.go, internal/rpc/oss377_managed_fixture_test.go]
sources:
  - title: Connect the managed agent
    resource: ../../docs/node-installer.md
  - title: ADR 0019 node control contract
    resource: ../../docs/adr/0019-node-control-contract.md
---

The `carbon-node run` entry point consumes the portable installer's existing
identity, approved enrollment bundle and one-time secret. Live claim material
is prepared in memory with a persistent one-time reservation. The challenge
and claim use one direct HTTPS connection with the shared TLS exporter.
Authentication for reporting uses a separately negotiated mTLS connection.

The agent verifies the issuing CA and node key before storing the public
credential. It probes the qualified private Unix socket, checks the engine's
actual data root and pins its daemon ID. A lost or changed daemon is reported
as unavailable under the original identity. The agent submits preflight and
health using the issued credential and endpoint generations.

`internal/rpc/oss377_managed_fixture_test.go` (Linux-only) exercises this real
agent against the shared enrollment authority over loopback TLS 1.3: challenge,
TLS-bound claim, scoped mTLS, preflight, heartbeat, restart reuse and refused
anonymous/unenrolled/cross-node/revoked credentials. Its daemon is a test-owned
Docker HTTP API, and its post-enrollment control RPCs are test-only because
production keeps `NodeControlService` unmounted. It is not evidence of a live
VM guest, a real daemon or a deployed controller.
