---
type: Architecture
title: "Node control protocol — Rule (part 2)"
description: "The contract ships without an implementation. New RPCs are denied by default"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/node_control.proto, pkg/proto/carbonpanel/v1/carbonpanelv1connect/node_control.connect.go, internal/db/models.go, internal/db/migrations.go, internal/rpc/node_control_contract_test.go]
sources:
  - title: Parent concept
    resource: node-control-protocol.md
---
- The contract ships without an implementation. New RPCs are denied by default
  by the shared interceptor until a permission is mapped; the negative fixture
  proves even broad grants and no-auth admin are refused.
