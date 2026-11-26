---
type: Architecture
title: "Node control protocol — Rule (part 2)"
description: Node control stays separate from the panel RPC surface and uses explicit enrollment domains.
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/node_control.proto, pkg/proto/carbonpanel/v1/carbonpanelv1connect/node_control.connect.go, internal/db/models.go, internal/db/migrations.go, internal/rpc/node_control_contract_test.go]
sources:
  - title: Parent concept
    resource: node-control-protocol.md
---
- The general panel interceptor denies node-control RPCs without mappings.
  The separate enrollment handler requires direct TLS 1.3. Creation and
  revocation require an authenticated operator session and exact node-scoped
  create or update permission. Bootstrap accepts only challenge and claim
  calls on their bound TLS connection. The main panel listener has no route
  for this handler. See [ADR 0019](../../docs/adr/0019-node-control-contract.md).
