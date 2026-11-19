---
type: Architecture
title: Node control protocol
description: A versioned typed contract for managed nodes; responses carry no secrets and routing never falls back to a legacy endpoint.
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/node_control.proto, pkg/proto/carbonpanel/v1/carbonpanelv1connect/node_control.connect.go, internal/db/models.go, internal/db/migrations.go, internal/rpc/node_control_contract_test.go]
sources:
  - title: ADR 0019 node control contract
    resource: ../../docs/adr/0019-node-control-contract.md
  - title: ADR 0018 node response redaction
    resource: ../../docs/adr/0018-node-response-redaction.md
---

# Parts

- [Node control protocol — Rule](node-control-protocol-rule.md) — Managed-node control uses the separate versioned contract in
- [Node control protocol — Rule (part 2)](node-control-protocol-rule-2.md) — The contract ships without an implementation. New RPCs are denied by default
- [Node control protocol — Why + How to check + Read next](node-control-protocol-why.md) — A direct Docker endpoint is not an authenticated node principal and has no
