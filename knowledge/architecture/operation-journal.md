---
type: Architecture
title: Operation journal
description: Lifecycle mutations are journalled before the Docker call, so an interrupted request leaves evidence and a release path instead of a half-changed server.
status: stable
owners: [oss-runtime]
code_refs: [internal/operation, internal/activity, internal/db, internal/rpc/services/operation.go, internal/rpc/services/activity.go, internal/rpc/services/support.go, internal/rpc/services/server.go, web/carbon-panel/src/lib/components/server-operations.svelte, web/carbon-panel/src/lib/components/server-activity.svelte]
sources:
  - title: Server operations runbook
    resource: ../../docs/runbook-server-operations.md
---

# Parts

- [Operation journal — Shape + Rules + Read next](operation-journal-shape.md) — Every create, start, stop, restart, delete and migrate is written to

# Key facts

- journal row and an activity event join. Listing activity for one server
- The journal is readable only through a server: every RPC carries a
- A journaled mutation that writes an activity row stores the client
- never returns another server's rows or request ids. Redaction of recorded
- Generate and Upload always attach `operations.json` (the projected journal)
- and `observations.json` (per-server stale vs last-good join evidence). Writing
- `GetServer` projects that same join classification onto live fields.
