---
type: Architecture
title: RPC contract
description: Proto definitions are the single API contract for the backend, console and CLI.
status: stable
owners: [oss-runtime]
code_refs:
  - proto/carbonpanel/v1
  - internal/rpc
  - buf.gen.yaml
  - web/carbon-panel/src/lib/proto
  - internal/nodescan
sources:
  - title: Design specification
    resource: ../../DESIGN.md
---

# Rule

Every frontend or CLI call goes through a service defined in
`proto/carbonpanel/v1`. Do not add an ad-hoc HTTP endpoint for product data.
Existing product HTTP consumers migrate incrementally through shared behavior
and tested compatibility adapters. [ADR 0012](../../docs/adr/0012-product-transports.md)
defines the minimum deprecation window and explicit content/integration HTTP
exceptions; [the inventory](../../docs/product-transports.md) identifies active
families. The rule describes the destination contract, not permission to remove
live REST consumers without a migration.
Node discovery is the first compatibility migration: both transports use the
same twelve-second scan and best-effort known-node count. ScanNodes requires
nodes/read globally; the compatibility route preserves numeric JSON fields and
denies missing authentication or authorization dependencies. A node-scoped grant
cannot authorize a host-wide scan.

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
