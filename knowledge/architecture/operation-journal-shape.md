---
type: Architecture
title: "Operation journal — Shape + Rules + Read next"
description: "Every create, start, stop, restart, delete and migrate is written to"
status: stable
owners: [oss-runtime]
code_refs: [internal/operation, internal/activity, internal/db, internal/rpc/services/operation.go, internal/rpc/services/activity.go, internal/rpc/services/support.go, internal/rpc/services/server.go, web/carbon-panel/src/lib/components/server-operations.svelte, web/carbon-panel/src/lib/components/server-activity.svelte]
sources:
  - title: Parent concept
    resource: operation-journal.md
---
# Shape

Every create, start, stop, restart, delete and migrate is written to
`server_operations` before the Docker call and advanced afterwards. The row is
`prepared` while the intent is recorded, `executing` when the attempt starts,
and `complete`, `failed` or `recovered` when it settles. A per-server unique
index on the active lock stops two lifecycle mutations of one world interleaving.

`operation.Describe` projects a row into one outcome — `success`, `failed`,
`running` or `unknown` — plus a stable reason and next-action code. `unknown` is
first-class: an unrecorded external effect is neither success nor failure. Stale
`executing` and uncertain-effect rows are `unknown` with reason codes
`executing_stale` / `uncertain_external_effect` and `resolve_operation`, never a
blind retry.

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
- Operator projections expose node id, endpoint generation, request key, phase
  and timestamps, plus correlation `matched`, `unmatched` or `unknown`. A raw
  Docker daemon id is never serialized (wire, panel, `-operations list`,
  `operations.json`); missing observation is `unknown`, not a match. See
  [ADR 0058](../../docs/adr/0058-operation-daemon-id-disclosure.md).
- The journal is readable only through a server: every RPC carries a
  `server_id` that is the RBAC object id, and an empty one is not a wildcard.
- A journaled mutation stores the same client `request_id` on its activity row,
  so the two join. Activity listing stays server-scoped; redaction is unchanged.
- Generate and Upload attach `operations.json` (projected journal) and
  `observations.json` (stale vs last-good join evidence).
- `GetServer` projects that join classification onto live fields; last-good SLP
  is not playable state. See [actionable recovery](../operations/actionable-recovery.md).

# Read next

- [Runtime model](runtime.md)
- [Fail-closed authorization](../invariants/authz-fail-closed.md)
- [Data safety](../invariants/data-safety.md)
- [Actionable recovery](../operations/actionable-recovery.md)
