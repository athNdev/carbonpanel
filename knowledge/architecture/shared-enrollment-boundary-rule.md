---
type: Architecture
title: "Shared enrollment boundary — Rule + Why"
description: "internal/noderuntime/enroll is the SINGLE mode-agnostic production boundary"
status: stable
owners: [oss-runtime]
code_refs: [internal/noderuntime/enroll, internal/noderuntime/native/agent.go, internal/nodeinstall, internal/rpc/node_control_enrollment_fixture_test.go, internal/rpc/enrollment_integration_test.go]
sources:
  - title: Parent concept
    resource: shared-enrollment-boundary.md
---
# Rule

`internal/noderuntime/enroll` is the SINGLE mode-agnostic production boundary
for managed-node enrollment and authorization (OSS-385). Host-native, VM and
LXC lanes consume it; they do not fork `node_control.proto`, reimplement the
claim context, add a second certificate policy or duplicate a domain check.

- `ClaimContextRaw` is the only byte encoder for `SIG_CLAIM`. The nonce is the
  server-stored challenge nonce (the shared proto has no client nonce field),
  the signed expiry is the enrollment `expires_at`, and the nonce's shorter TTL
  is server-only. Fields use explicit `0x00` delimiters; capability names are
  comma-joined proto enum names. The node-installer's `ClaimContext` is only a
  normalising adapter over this encoder (short bundle names become proto enum
  names); a mode-local encoder is a defect because the controller verifies with
  this one and a divergent claim is refused.
- `internal/rpc/enrollment_integration_test.go` proves the seam end-to-end in an
  owned fixture: real installer-generated proto claims verify at the shared
  `Boundary`, a divergent node-local encoding cannot bypass it, and Ed25519,
  approved key, server nonce, atomic consume/rollback and domain separation hold.
  No production RPC is mounted and no live trust boundary or deployment is
  claimed.
- The scheme is Ed25519-only. P-256 is refused at registration; adding a second
  curve needs a new signing domain and a new ADR.
- Certificate issuance happens BEFORE any ownership mutation. A failed
  issuance changes nothing (state, nonce, generation, serial counter) and a
  later legitimate retry with a fresh challenge succeeds. `InjectIssuanceFailure`
  and `InjectPreCommitFailure` are owned fault seams.
- The shared `Boundary` wires all three non-crossing domains: operator for
  enrollment management, bootstrap phase marker for the two bounded enrollment
  steps, enrolled node for operations on its own `object_id`. Only a digest of
  the node secret is stored and no read response carries a secret.
- Nothing here mounts a production RPC, probes a host, installs a daemon or
  deploys anything. Keys and CAs are owned/synthetic; fixtures prove refusal
  and state-machine outcomes only.

# Why

Parallel deployment modes previously each risked reimplementing proof of
possession against the shared contract. One package keeps the signing context,
issuance order and domain separation identical across modes and makes the
atomicity defect independently testable without a live controller.
