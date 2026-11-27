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
bytes with a SHA-512 check, and stages them into the server's open batch without
writing the live mods directory or restarting the server.

JSON-backed `mod_update` policies use the same planner; save waits for the task list, and policy edits preserve schedule state with opt-outs starting disabled.

# Consequences

- The panel binds each reviewed selection to the candidate version id, SHA-512
  and filename it was selected with; a refresh returning a different candidate
  clears that row's selection, so a new release cannot be staged under the earlier review.
- Drift, provider, download and checksum mismatches are per-item refusals;
  other selected updates still stage. The reviewed installed digest selects the
  actual live copy, and two matching copies are refused as ambiguous.
- A required or incompatible dependency on the fetched release refuses the
  update even when its version id, filename and SHA-512 match the preview.
- A temporary release lookup is a retryable download refusal; a confirmed
  missing, withdrawn or invalid release is a provider refusal. A per-item
  staging storage/IO failure leaves earlier rows committed and returns a retryable policy error; validation refusals stay per-item.
- Publishing, backup, rollback, apply windows and per-server serialization are
  the existing staged-batch guarantees. Durable approval, the data-write lease
  and window checks are in [Reviewed mod update publication](mod-update-publication.md).
- The update flow extends `UpdateMod` instead of adding an RPC, so it reuses `mods/update[server_id]` and is not exposed as an MCP tool.
- A disabled policy, missing planner, bad config or already-staged file cannot stage a duplicate or an unchecked jar.
- The guarded HTTP client protects egress and redirects. Fetch and staging
  bound bytes. Retries skip staged files and preserve failure at budget exhaustion.
- Journaled admission recomputes and persists the normalized runtime target; a
  moved target or direct non-staged runtime save is refused while a candidate is
  staged. An open-batch file is `already_staged` before download and spends no
  budget; the under-journal recheck refuses a late duplicate, and a resubmission
  names the batch only after confirming it holds every reported file. A file no
  longer pending after a discard is retryable and is not staged here.
- Due slots and execution claims are in
  [Mod update scheduling and legacy rows](mod-update-scheduling.md).
