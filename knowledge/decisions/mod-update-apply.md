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
- Window, staged rows and target uniqueness are journaled together; unrelated
  reviewed batches are not retargeted. The whole window tuple is compared
  before work, duplicate targets and cumulative bytes are bounded, and the
  batch/config window transition is transactional. The full tuple is rechecked
  under the operation, inserts name the still-open chosen batch, discard shares
  the lock, cron config rows keep their schedule, and rejected transfers still
  consume the byte budget. A batch with a cron config row is refused by both
  mod-staging entry points, preserving that row's batch and trigger; a cron
   row cannot join a timed mod batch via transactional admission; legacy mixed
   data is isolated by the publisher (cron stays staged; no new poller). Discard
   keeps typed Connect refusals and its own operation name. The guarded World
   Library client is reused and transient provider failures stay retryable.
- The scheduler installs the planner before starting, applies timezone only to
  cron, and refuses to dispatch a task whose next run cannot be advanced.
