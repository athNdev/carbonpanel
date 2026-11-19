---
type: Architecture
title: "Shared enrollment boundary — How to check + Read next"
description: "go test ./internal/noderuntime/... ./internal/nodeinstall/... ./internal/rpc/... ./internal/db/..."
status: stable
owners: [oss-runtime]
code_refs: [internal/noderuntime/enroll, internal/noderuntime/native/agent.go, internal/nodeinstall, internal/rpc/node_control_enrollment_fixture_test.go, internal/rpc/enrollment_integration_test.go]
sources:
  - title: Parent concept
    resource: shared-enrollment-boundary.md
---
# How to check

- `go test ./internal/noderuntime/... ./internal/nodeinstall/... ./internal/rpc/... ./internal/db/...`
  and `go vet` on those packages plus `cmd/carbon-node`. The integration test
  must keep the installer context byte-identical to `ClaimContextRaw`.
- The exact context encoding, curve decision and issue-first rule are in
  [ADR 0019](../../docs/adr/0019-node-control-contract.md).

# Read next

- [Host-native private daemon mode](host-native-runtime.md)
- [Node control protocol](node-control-protocol.md)
