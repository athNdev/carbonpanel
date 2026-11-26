---
type: Decision
title: Reviewed mod update application
description: Manual and scheduled mod updates share one staged-batch planner; bytes are checked before staging and never published by the planner.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/rpc/services/mod_update_apply.go
  - internal/rpc/services/mod_update_policy.go
  - internal/rpc/services/mod.go
  - internal/scheduler/scheduler.go
  - internal/db/staged_content.go
  - internal/db/staged_config.go
  - internal/db/models.go
  - proto/carbonpanel/v1/mod.proto
  - proto/carbonpanel/v1/task.proto
  - web/carbon-panel/src/lib/components/server-mods.svelte
  - web/carbon-panel/src/lib/mod-update-ui.ts
sources:
  - title: ADR 0053
    resource: ../../docs/adr/0053-reviewed-mod-update-application.md
---

# Choice

An operator selects an already-previewed compatible release; the RPC rechecks
the installed fingerprint and provider release, downloads the exact candidate
bytes with a SHA-512 check, and stages them into the server's open batch. The
planner never writes the live mods directory or restarts the server.

JSON-backed `mod_update` policies use the same planner. Save waits for the
task list. Policy edits preserve schedule state and opt-outs start disabled.

# Consequences

- Drift, provider, download and checksum mismatches are per-item refusals;
  other selected updates still stage.
- Publishing, backup, rollback, apply windows and per-server serialization are
  the existing staged-batch guarantees.
- The update flow extends `UpdateMod` rather than adding an RPC, so it reuses
  `mods/update[server_id]` and is not exposed as an MCP tool.
- A disabled policy, missing planner, bad config or already-staged file cannot
  stage a duplicate or an unchecked jar.
- Admission records the operator-approved provider SHA-512 in the staged row
  only after the staged bytes are verified against it. A reviewed row from
  before durable approval is refused with re-review and re-stage guidance
  instead of trusting a mutable sidecar. Unreviewed uploads keep their path.
- Apply revalidates the preview. Publish verifies the reviewed live SHA-512 on
  the undo stream and requires the durable approved digest on the publication
  stream, preserving staged bytes on drift.
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
- The guarded HTTP client protects egress and redirects. Fetch and staging
  bound bytes. Retries skip staged files and preserve failure at budget exhaustion.
- Mod-update dispatch requires the observed deadline, schedule, status and
  policy revision. A one-shot claim records its execution with the disabled
  slot in the same guarded write; startup recovery closes an interrupted claim
  and an insert failure keeps it due. The scheduler installs the planner first
  and refuses invalid policy windows.
