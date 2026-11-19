---
type: Runbook
title: "Server lifecycle journal — Rules (part 2)"
description: "The support bundle always carries operations.json with that projection and"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: lifecycle-journal.md
---
- The support bundle always carries `operations.json` with that projection and
  `observations.json` with per-server last-good timestamps, so a bundle can
  explain a stuck or stale server without the Docker socket. Operator next
  actions for permission, port, pull, stale node, partial restore/migration
  and disk exhaustion are in [actionable recovery](actionable-recovery.md).
