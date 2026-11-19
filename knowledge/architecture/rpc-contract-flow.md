---
type: Architecture
title: "RPC contract — Flow + Invariants + Read next"
description: "Regenerate with make proto (Docker + pinned Buf); generation and formatting"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1, internal/rpc, buf.gen.yaml, web/carbon-panel/src/lib/proto, internal/nodescan]
sources:
  - title: Parent concept
    resource: rpc-contract.md
---
# Flow

1. Edit the `.proto` file.
2. Regenerate with `make proto` (Docker + pinned Buf); generation and formatting
   complete in a staging directory before tracked outputs are replaced. See
   [development](../../docs/development.md) for the native comparison command.
3. Implement the handler in `internal/rpc/handlers` and the service in
   `internal/rpc/services`.
4. Map the RPC to a permission and add negative authorization tests.
5. Consume the generated TypeScript client from
   `web/carbon-panel/src/lib/proto`.

# Invariants

- New RPCs are denied by default until a permission is mapped.
- Request and response types are shared; do not hand-write a parallel DTO.
- Security headers and middleware live in `internal/rpc`, not in handlers.

# Read next

- [Add an RPC](../workflows/add-rpc.md)
- [Fail-closed authorization](../invariants/authz-fail-closed.md)
