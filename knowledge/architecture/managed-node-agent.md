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

`internal/rpc/oss377_managed_fixture_test.go` exercises the agent on Linux with
the shared enrollment authority over loopback TLS 1.3. It covers TLS-bound
claim, scoped mTLS, preflight, a periodic heartbeat, fresh reports after agent
restart and denial of anonymous, unenrolled, cross-node and revoked clients.
The fixture supplies its own listener, in-memory enrollment store, control RPCs
and Docker HTTP API. VM guest enrollment with the deployed controller and a
real daemon requires separate evidence.
