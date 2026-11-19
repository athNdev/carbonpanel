---
type: Architecture
title: "Node control protocol — Why + How to check + Read next"
description: "A direct Docker endpoint is not an authenticated node principal and has no"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/node_control.proto, pkg/proto/carbonpanel/v1/carbonpanelv1connect/node_control.connect.go, internal/db/models.go, internal/db/migrations.go, internal/rpc/node_control_contract_test.go]
sources:
  - title: Parent concept
    resource: node-control-protocol.md
---
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
- The P1 contract fields and bounded challenge procedure are present in the
  proto; `node_control_enrollment_fixture_test.go` is a **test-only** seam, not
  an enrollment server. No enrollment RPC handler is mounted and no deployment
  or live-node success may be claimed from these fixtures.
- Before any node-control RPC is mounted in implementation, the harmless P1
  enrollment fixtures in ADR 0019 must pass: synthetic key/`CSR_PRE` and fresh
  signature, valid claim, mismatched approved key, stale/replayed nonce,
  replay refusal, revoked/generation-mismatch refusal, wrong-scope and
  preflight-scope refusal, unsupported-topology refusal, digest-only storage,
  no secret in read responses, atomicity and durable rotation across restart.
  They use synthetic keys, fake TLS channels and owned in-memory/local DB
  fixtures only.
- Review new RPCs against `knowledge/workflows/add-rpc.md`: no mapping means
  denied, and no handler should be reachable in the negative fixture.

# Read next

- [ADR 0019](../../docs/adr/0019-node-control-contract.md)
- [Node resolution](node-resolution.md)
- [RPC contract](rpc-contract.md)
