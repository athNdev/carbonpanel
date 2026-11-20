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

Automatic and scheduled policies are `mod_update` tasks holding a JSON config.
The scheduler invokes the same planner through an injected interface.

# Consequences

- Drift, provider, download and checksum mismatches are per-item refusals;
  other selected updates still stage.
- Publishing, backup, rollback, apply windows and per-server serialization are
  the existing staged-batch guarantees.
- The update flow extends `UpdateMod` rather than adding an RPC, so it reuses
  `mods/update[server_id]` and is not exposed as an MCP tool.
- A disabled policy, missing planner, bad config or already-staged file cannot
  stage a duplicate or an unchecked jar.
- Apply revalidates the server-owned preview and the live digest; publish
  refuses a delayed-drift row and keeps staged bytes for recovery.
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
  bound candidate and total bytes, including rejected downloads.
- The scheduler installs the planner first, uses timezones for cron, and
  skips dispatch if it cannot advance the task's next run.
