---
type: Runbook
title: Server lifecycle journal
description: Interrupted container mutations stay locked until the daemon is observed, and an operator can release that lock without deleting containers.
status: stable
owners: [oss-runtime]
code_refs: [internal/operation, internal/activity, internal/db/server_operation.go, cmd/carbon-panel/operations.go, internal/db/portlease.go, internal/rpc/services/support.go]
sources:
  - title: Server operation runbook
    resource: ../../docs/runbook-server-operations.md
---

# Parts

- [Server lifecycle journal — Use this when](lifecycle-journal-use-this-when.md) — A server lifecycle action must survive a panel crash, or a server is stuck
- [Server lifecycle journal — Rules](lifecycle-journal-rules.md) — Persist the intent before calling Docker. If the intent cannot be stored,
- [Server lifecycle journal — Rules (part 2)](lifecycle-journal-rules-2.md) — The support bundle always carries operations.json with that projection and
- [Server lifecycle journal — Read next](lifecycle-journal-read-next.md) — Detailed guidance on read next.
