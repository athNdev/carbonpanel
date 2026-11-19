---
type: Architecture
title: "Node control protocol — Rule"
description: "Managed-node control uses the separate versioned contract in"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/node_control.proto, pkg/proto/carbonpanel/v1/carbonpanelv1connect/node_control.connect.go, internal/db/models.go, internal/db/migrations.go, internal/rpc/node_control_contract_test.go]
sources:
  - title: Parent concept
    resource: node-control-protocol.md
---
# Rule

Managed-node control uses the separate versioned contract in
`proto/carbonpanel/v1/node_control.proto`. It is the only place node identity,
enrollment state, per-node claims, capabilities, health, operation envelopes
and dispatch outcomes are defined for the agent path.

- Identity and responses carry fingerprints, generations and metadata only. No
  token, private key or TLS secret appears in a response message.
- Enrollment is one-time, TTL-bounded and **digest-only** on the controller.
  The node generates its own high-entropy secret `S`, salt and key pair; `S`
  stays on the node and is sent only once in the claim. The operator registers
  a REQUIRED approved public key (SPKI + fingerprint), salt and digest out of
  band. The pre-registration CSR `CSR_PRE` is material only; proof of possession
  is a **fresh** signature by the approved key after a bounded, unauthenticated
  `BeginEnrollmentChallenge` issues a single-use server nonce. A signature from
  any other key, a caller-declared fingerprint or a captured token is refused.
- Claim is an atomic compare-and-consume in one transaction over the exact
  context (node ID, enrollment ID, challenge ID/nonce, expiry, capabilities,
  verified channel context); replay, revoked state, expired TTL, stale nonce
  and generation mismatch are refused. The controller never emits or stores a
  plaintext enrollment secret.
- The enrolled credential is a **signed public client certificate** bound to
  the approved node key and used with mTLS; the private key never leaves the
  node, and no read response carries a secret. Channel binding must be derived
  by the controller or verified from a configured trusted TLS terminator;
  client-supplied headers/assertions alone are never trusted and unsupported
  topologies are refused with `UNSUPPORTED_TLS_TOPOLOGY`.
- Three authorization domains never cross: operator provisioning
  (`nodes:create`/`nodes:update`), one-time bootstrap
  (`BeginEnrollmentChallenge` + one `ClaimEnrollment`), and enrolled node
  (mTLS certificate with per-node scopes on its own object). A bootstrap secret
  is not a node credential; an operator cannot act as a node. Rotation and
  revocation are durable across restart.
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
