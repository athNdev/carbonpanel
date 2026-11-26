---
type: Decision
title: Node enrollment state
description: Preserve enrollment and revocation across controller restart with a signer-bound SQLite compare-and-swap.
status: stable
owners: [oss-runtime]
code_refs: [internal/noderuntime/enroll/persistence.go, internal/db/node_enrollment_authority_state.go, internal/db/migrations.go]
sources:
  - title: ADR 0019 node control contract
    resource: ../../docs/adr/0019-node-control-contract.md
---
# Decision

- Track enrollment, challenge and revocation in one versioned SQLite record
  so the authority publishes each complete transition only after commit.
- Bind the record to the operator-provided signing CA digest. Keep the
  private signer and the node's plaintext enrollment secret out of storage.
- Authenticate the current active grant by the exact verified client
  certificate digest, serial and generation. Store failures deny access.
