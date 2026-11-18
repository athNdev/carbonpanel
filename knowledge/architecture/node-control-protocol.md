---
type: Architecture
title: Node control protocol
description: A versioned typed contract for managed nodes; responses carry no secrets and routing never falls back to a legacy endpoint.
status: stable
owners: [oss-runtime]
code_refs:
  - proto/carbonpanel/v1/node_control.proto
  - pkg/proto/carbonpanel/v1/carbonpanelv1connect/node_control.connect.go
  - internal/db/models.go
  - internal/db/migrations.go
  - internal/rpc/node_control_contract_test.go
sources:
  - title: ADR 0019 node control contract
    resource: ../../docs/adr/0019-node-control-contract.md
  - title: ADR 0018 node response redaction
    resource: ../../docs/adr/0018-node-response-redaction.md
---

# Rule

Managed-node control uses the separate versioned contract in
`proto/carbonpanel/v1/node_control.proto`. It is the only place node identity,
enrollment state, per-node claims, capabilities, health, operation envelopes
and dispatch outcomes are defined for the agent path.

- Identity and responses carry fingerprints, generations and metadata only. No
  token, private key or TLS secret appears in a response message.
- Enrollment is one-time, TTL-bounded and **digest-only** on the controller.
  The node generates its own high-entropy secret and key pair, registers only a
  salt plus digest out of band through an operator, and proves possession with
  a signed CSR over the exact enrollment context. A caller-declared key
  fingerprint or a captured bootstrap token is never proof of identity.
- Claim is an atomic compare-and-consume in one transaction, bound to a bounded
  session and the TLS channel; replay, revoked state, expired TTL and
  generation mismatch are refused. The controller never emits or stores a
  plaintext enrollment secret.
- Three authorization domains never cross: operator provisioning
  (`nodes:create`/`nodes:update`), one-time bootstrap (`ClaimEnrollment` only),
  and enrolled node (per-node scopes on its own object). A bootstrap secret is
  not a node credential; an operator cannot act as a node.
- Existing and new nodes stay `legacy` and unenrolled until an operator
  explicitly opts in. No success evidence may be claimed from an unimplemented
  stub; P0/P1 fixtures prove denial, contract shape and the enrollment state
  machine only.
- Capability negotiation and an explicit supported-version range gate
  operations. Version skew is an `UPGRADE_REQUIRED` outcome, not silent
  degradation.
- Operations carry an idempotency key, request hash, endpoint and credential
  generations, deadline, attempt and capability snapshot. `UNCERTAIN` blocks
  automatic retry.
- `DispatchOperation` fails closed. An unspecified, non-managed or unresolvable
  node returns an unavailable reason; there is no controller-local, legacy
  endpoint or host-socket fallback. This extends
  [node resolution](node-resolution.md) rather than replacing it.
- The contract ships without an implementation. New RPCs are denied by default
  by the shared interceptor until a permission is mapped; the negative fixture
  proves even broad grants and no-auth admin are refused.

# Why

A direct Docker endpoint is not an authenticated node principal and has no
version or capability. Without a typed contract, agent work would invent
parallel DTOs, weaken the redaction boundary or silently fall back to a legacy
host.

# How to check

- `buf lint` and `buf format -d` on the new proto; `bun scripts/proto-generate.mjs --check`
  for reproducible Go/TS output. Only Track C regenerates from this proto.
- `go test ./internal/rpc -run TestNodeControl` and
  `go test ./internal/db -run TestNodeControlContract`.
- Before any node-control RPC is mounted in implementation, the harmless P1
  enrollment fixtures in ADR 0019 must pass: synthetic key/CSR, valid claim,
  replay refusal, revoked/generation-mismatch refusal, wrong-scope refusal,
  digest-only storage and no secret in read responses. They use synthetic keys,
  fake TLS channels and owned in-memory/local DB fixtures only.
- Review new RPCs against `knowledge/workflows/add-rpc.md`: no mapping means
  denied, and no handler should be reachable in the negative fixture.

# Read next

- [ADR 0019](../../docs/adr/0019-node-control-contract.md)
- [Node resolution](node-resolution.md)
- [RPC contract](rpc-contract.md)
