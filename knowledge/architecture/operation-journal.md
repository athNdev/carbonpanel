---
type: Architecture
title: Operation journal
description: Lifecycle mutations are journalled before the Docker call, so an interrupted request leaves evidence and a release path instead of a half-changed server.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/operation
  - internal/activity
  - internal/db
  - internal/rpc/services/operation.go
  - internal/rpc/services/activity.go
  - web/carbon-panel/src/lib/components/server-operations.svelte
  - web/carbon-panel/src/lib/components/server-activity.svelte
sources:
  - title: Server operations runbook
    resource: ../../docs/runbook-server-operations.md
---

# Shape

Every create, start, stop, restart, delete and migrate is written to
`server_operations` before the Docker call and advanced afterwards. The row is
`prepared` while the intent is being recorded, `executing` when the attempt
starts, and `complete`, `failed` or `recovered` when it settles. A per-server
unique index on the active lock means two lifecycle mutations of one world
cannot interleave.

`operation.Describe` projects a row into exactly one outcome — `success`,
`failed`, `running` or `unknown` — plus a stable next-action code. `unknown` is
a first-class answer: a mutation whose external effect was never recorded is
neither a success nor a failure, and collapsing it into either is the bug this
exists to prevent.

# Rules

- A mutation that reached Docker but recorded no completion is `unknown` until
  something observes the container. Startup recovery observes first, then
  settles the row.
- `executing` reads as `running` only while the row was advanced within
  `operation.ExecutionStaleAfter`; after that it degrades to `unknown` with the
  `resolve_operation` escape hatch.
- Releasing a lock never touches containers, files or worlds. It records an
  operator note, writes an audit entry and nudges the reconciler.
- Recorded error text is redacted for credentials, never for hosts, paths or
  daemon messages: those are the evidence.
- The journal is readable only through a server: every RPC carries a
  `server_id` that is the RBAC object id, and an empty one is not a wildcard
  grant.
- A journaled mutation that writes an activity row stores the client
  `request_id` on that row. `operation.Describe` exposes the same id so a
  journal row and an activity event join. Listing activity for one server
  never returns another server's rows or request ids. Redaction of recorded
  error text is unchanged.

# Read next

- [Runtime model](runtime.md)
- [Fail-closed authorization](../invariants/authz-fail-closed.md)
- [Data safety](../invariants/data-safety.md)
