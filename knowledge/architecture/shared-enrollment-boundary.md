---
type: Architecture
title: Shared enrollment boundary
description: One mode-agnostic production package owns managed-node enrollment, canonical claim signing, issuance and the three authorization domains.
status: stable
owners: [oss-runtime]
code_refs: [internal/noderuntime/enroll, internal/noderuntime/native/agent.go, internal/nodeinstall, internal/rpc/node_control_enrollment_fixture_test.go, internal/rpc/enrollment_integration_test.go, internal/rpc/oss377_managed_fixture_test.go]
sources:
  - title: ADR 0019 node control contract
    resource: ../../docs/adr/0019-node-control-contract.md
---

# Parts

- [Shared enrollment boundary — Rule + Why](shared-enrollment-boundary-rule.md) — internal/noderuntime/enroll is the SINGLE mode-agnostic production boundary
- [Shared enrollment boundary — How to check + Read next](shared-enrollment-boundary-how-to-check.md) — go test ./internal/noderuntime/... ./internal/nodeinstall/... ./internal/rpc/... ./internal/db/...

# Key facts

- for managed-node enrollment and authorization (OSS-385). Host-native, VM and
- LXC lanes consume it; they do not fork `node_control.proto`, reimplement the
- `ClaimContextRaw` is the only byte encoder for `SIG_CLAIM`. The nonce is the
- the signed expiry is the enrollment `expires_at`, and the nonce's shorter TTL
- `internal/rpc/enrollment_integration_test.go` proves the seam end-to-end in an
- `Boundary`, a divergent node-local encoding cannot bypass it, and Ed25519,
- The shared `Boundary` wires all three non-crossing domains: operator for
- enrollment management, bootstrap phase marker for the two bounded enrollment
- `internal/noderuntime/enroll` is the SINGLE mode-agnostic production boundary
- claim context, add a second certificate policy or duplicate a domain check.
- server-stored challenge nonce (the shared proto has no client nonce field),
- comma-joined proto enum names. The node-installer's `ClaimContext` is only a
- this one and a divergent claim is refused.
- owned fixture: real installer-generated proto claims verify at the shared
- approved key, server nonce, atomic consume/rollback and domain separation hold.
- No production RPC is mounted and no live trust boundary or deployment is
- steps, enrolled node for operations on its own `object_id`. Only a digest of
- the node secret is stored and no read response carries a secret.
- possession against the shared contract. One package keeps the signing context,
- and `go vet` on those packages plus `cmd/carbon-node`. The integration test
- The exact context encoding, curve decision and issue-first rule are in
- [ADR 0019](../../docs/adr/0019-node-control-contract.md).
- [Node control protocol](node-control-protocol.md)
