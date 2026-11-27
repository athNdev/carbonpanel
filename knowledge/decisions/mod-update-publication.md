---
type: Decision
title: Reviewed mod update publication
description: A staged reviewed mod row carries durable approval and publishes under the data-write lease and window checks.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/staging/file_undo.go
  - internal/staging/apply.go
  - internal/db/staged_content.go
  - internal/db/staged_config.go
sources:
  - title: ADR 0053
    resource: ../../docs/adr/0053-reviewed-mod-update-application.md
---
# Choice

A reviewed mod row records the provider release identity and SHA-512 the
operator approved at admission, and publication checks that durable evidence
instead of a mutable sidecar. Staged publication owns the server data path
exclusively from before the undo snapshot through rollback.

# Consequences

- Admission records the approved provider release identity, SHA-512 and the
  normalized effective Docker loader/version in the staged row only after the
  staged bytes match. A row without durable approval or runtime-target
  provenance is refused with re-review and re-stage guidance; unreviewed
  uploads keep their path. Staging uses the effective Docker `ServerConfig`
  target, not the stale `Server` projection, and a runtime rollback or direct
  runtime-target save is refused while a reviewed candidate is staged.
- Publication revalidates the persisted row target against the current
  effective target before any live-tree mutation. A moved target is a typed
  `FailedPrecondition` refusal that preserves live and staged bytes.
- Publish requires the durable identity on the publication stream and verifies
  the reviewed live SHA-512 on the undo stream; drift preserves staged bytes.
- Staged publication holds the per-server data-write lease exclusively from
  before the undo snapshot through rollback; see
  [Data-write gate](data-write-gate.md).
- Reviewed content checks the complete window before work, at batch
  preparation, and at row insertion. Config targeting an open batch with
  content or an independent cron row must match its complete window. A
  config-only retarget resets the idle clock. Admission snapshots live config
  in the write transaction and excludes pending cron and content symmetrically.
- Config and content cannot join while publication or discard owns the
  journal. The publisher rechecks batch changes after acquiring the journal.
- Due publication of legacy mixed data excludes cron config from base checks,
  rollback reconciliation and publication. The remaining cron row and its
  trigger stay intact. Discard shares the operation lock, keeps its typed
  Connect refusals and records its own operation name.
