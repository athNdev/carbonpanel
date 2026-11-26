---
type: Decision
title: Mod update scheduling and legacy rows
description: Due mod updates claim their execution atomically; retained legacy cron config has no silent consumer.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/scheduler/scheduler.go
  - internal/db/staged_config.go
  - internal/db/models.go
sources:
  - title: Parent concept
    resource: mod-update-apply.md
  - title: ADR 0053
    resource: ../../docs/adr/0053-reviewed-mod-update-application.md
---

# Choice

Due publication of legacy mixed data excludes cron config from base checks,
rollback reconciliation and publication; the remaining cron row and trigger
stay intact. No production cron consumer applies those rows: they are pending
manual apply/discard actions or an explicit operator conversion, and must not
be silently re-enabled. Discard shares the lock, keeps typed Connect refusals
and names its operation.

# Consequences

- The guarded HTTP client protects egress and redirects. Fetch and staging
  bound bytes. Retries skip staged files and preserve failure at budget exhaustion.
- A provider lookup failure is `unavailable` with `Provider lookup failed: Try
  again later.`; diagnostics stay server-side and the prefix keeps apply/policy transient.
- Mod-update dispatch requires the observed deadline, schedule, status and
  policy revision. A due one-shot, cron or interval claim records its execution
  in the same guarded write as the slot; startup recovery closes an interrupted
  claim and an insert failure keeps the slot due. A one-shot slot also persists
  its terminal disabled state.
