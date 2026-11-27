---
type: Decision
title: Archive destination preflight authorization
description: Archive preview returns enabled destination IDs and names under servers/create instead of requiring nodes/read.
status: stable
owners: [oss-runtime]
code_refs:
  - proto/carbonpanel/v1/server.proto
  - internal/rpc/services/server_archive_import.go
  - web/carbon-panel/src/lib/features/servers/archive-import-session.ts
  - web/carbon-panel/src/routes/servers/new/+page.svelte
sources:
  - title: Server portability archive format
    resource: ../../docs/server-portability.md
---

# Decision

- `PreviewServerArchive` stays authorized by `servers/create`. Its response
  carries a repeated `ServerArchiveDestination` with only an enabled node's ID
  and display name.
- A caller denied `NodeService/ListNodes` can select one of those destinations,
  preview that exact node for capacity and submit the import to it.
- The preflight never carries addresses, credentials, capacity statistics or
  other node inventory, and it never qualifies create by itself. A supplied
  node still passes the exact-node enabled and free-space check.
- The wizard treats the current successful preflight as authoritative for the
  enabled-destination list, because it reflects nodes enabled now. It uses cached
  inventory only to seed the choice before an upload, preserves a selection that
  is still offered, and renders an actionable empty state with a preflight retry
  when the settled preflight has no enabled destination. The narrow preflight
  remains the only source for a create-only caller denied node inventory.

# Rationale

Archive import is a server-create action. Requiring node inventory read made a
create-only operator unable to select a valid destination, while broadening
`ListNodes` would expose deployment inventory to a role that does not need it.
An additive preview field keeps the existing upload ownership, permission
mapping and exact-node validation intact.
