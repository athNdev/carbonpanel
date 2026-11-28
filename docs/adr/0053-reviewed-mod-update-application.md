<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

# ADR 0053 — Reviewed mod updates reuse the staged batch

Status: Accepted (2026-11-20). Governs OSS-423, OSS-424 and OSS-425.

## Context

The installed-mod preview identifies a compatible Modrinth release by installed
SHA-512, project, loader, game version, release status, dependencies and a
candidate SHA-512. Applying that preview needs to preserve unrecognized or
modified uploads, survive provider and download failures, serialize with other
server mutations and leave an audit record. The repository already has one
journaled staged-batch publish path with per-file backups, undo, an apply
window and a durable per-server operation lock. A separate apply path would
duplicate those guarantees and could publish different bytes under the same
request.

## Decision

1. Candidate bytes are downloaded only after the installed file fingerprint and
   the provider release are re-verified. The download is egress-checked,
   size-bounded and SHA-512 verified against the reviewed candidate. A drift,
   provider, download or checksum mismatch produces a per-item refusal; the
   live file is never touched by staging.
2. Accepted candidates are written through the existing node-owned
   `.carbon-panel/staged/<id>/` path into the server's open update batch, using
   the installed file's live target path. Publishing stays with the existing
   journaled batch apply; no mod-specific apply path exists.
3. The typed RPC surface is an additive extension of `ModService.UpdateMod`,
   reusing its `mods/update[server_id]` mapping rather than introducing a new
   procedure. No MCP tool is exposed for this operator-only review flow.
4. Automatic and scheduled policies are `mod_update` `ScheduledTask` records.
   The scheduler holds a `ModUpdatePlanner` interface; the same service method
   implements manual and automatic staging. A nil planner, disabled policy,
   unknown apply window or malformed config fails closed.
5. Apply recomputes the server-owned preview for the reviewed files and refuses
   a stale project, version, dependency or installed-digest decision. Staged
   rows record the live SHA-512 observed at review time; the journaled publish
   verifies it on the same stream used to create the undo snapshot before
   mutating live files. It preserves staged bytes if the digest differs. Normal
   reviewed staging admission also records the provider release identity the
   operator approved (provider, project, version and exact candidate filename)
   together with its SHA-512, only after the bytes written to the stage
   directory have been verified against that digest. Apply validation requires
   that durable identity and digest to match the staged payload and file name;
   it does not select a version or project from a mutable sidecar. Staging uses
   the effective Docker `ServerConfig` type and version, not the panel `Server`
   projection, which can lag a config-only edit. Rolling a runtime config change
   back is itself a type/version change and is refused while a reviewed
   candidate is still staged. Rows staged
   before this rule have no durable approval or release identity and are
   refused with re-review and re-stage guidance instead of deriving either from
   the current mutable sidecar. Ordinary unreviewed uploads carry neither
   precondition and are unaffected. Publication requires the durable
   approved digest and release identity persisted at admission; the publish
   stream checks those stored values instead of rereading the staged sidecar,
   so metadata rewritten between admission and write cannot authorize
   different bytes. A mismatch refuses the publication without a live write
   or provider detail in the error.
6. The apply window is committed before the first staged byte is visible, and
   the window, staged rows and target-uniqueness check run under the existing
   per-server operation journal. A batch with unrelated reviewed changes under
   a different window is refused, not silently retargeted.
7. Downloads use the connection-guarded HTTP client shared with World Library
   imports. The fetcher and staging loop enforce per-file and total byte
   limits. Scheduled provider failures remain retryable after partial staging.
   Directory scan and entry metadata errors other than missing paths fail the
   run, while a per-file content read failure during the preview stays a
   per-item refusal. All retry attempts share the execution's update budget.
   Files that already have a staged row do not count as refusals.
8. The complete window tuple (mode, timestamp, idle interval) is compared
   before any provider work or persistent write; a differing window on a batch
   with reviewed changes is refused. Selections are deduplicated by live target
   and total candidate bytes are bounded so repeated or large requests cannot
   exhaust controller memory. The batch-plus-config window transition is one
   transaction and an unchanged window is not rewritten.
9. Reviewed content checks the full window tuple again when preparing its
   batch and when committing a staged row. An independent cron config and
   staged content cannot share a new batch admission in either arrival order.
   Each producer checks for the counterpart in the SQLite write transaction
   that creates its row. Config staging also compares the complete window of
   an open batch with content or an independent cron row, and refuses a
   mismatching request. Legacy config-only batches with no timed window
   still admit next-restart config alongside an independent cron row.
   On a config-only batch with no cron, an explicit
   retarget resets the idle clock when the window changes. Config staging
   snapshots the live baseline after admission, in the same write transaction.
   Config and content admission also check the publish and discard journal
   in their write transaction. A timed publisher rechecks its batch after
   obtaining the journal, so a retarget between selection and ownership
   waits for the new window. An open legacy batch can keep its pending cron rows while publishing due
   mod content: the publisher excludes cron config from base checks, rollback
   reconciliation and publication, then parks the remaining batch on next
   restart. Cron rows keep their own trigger and batch; this adds no cron
   executor. No production cron consumer is enabled, so retained legacy
   scheduled-config rows are pending manual apply/discard actions or an explicit
   operator conversion, and must not be silently re-enabled. Every transferred
   byte is charged to the budget, including rejected downloads. Discard shares
   operation ownership, preserves typed Connect refusals and records the discard
   operation under its own name.
10. Cron timezone is resolved only for cron schedules; if an existing task's
   schedule cannot be advanced, dispatch is refused. Mod-update advancement
   checks the observed deadline, schedule, status and policy revision to fence
   stale due reads after a reschedule or policy edit. A one-shot slot persists
   its disabled status and inserts its execution record in the same guarded
   write, so the post-claim revalidation sees one consistent state and the run
   dispatches exactly once. The same durable execution claim covers due cron and
   interval mod-update slots, not only one-shot policies. An interruption before
   the asynchronous dispatch leaves a running record that startup recovery
   closes, and an execution insert failure rolls the claim back so the slot
   stays due.
   Invalid current schedules are disabled. The planner is installed before the
   scheduler starts so cold boot sees the real implementation.

## Consequences

- Manual staging requires an operator to select a preview and apply window.
  An enabled scheduled policy selects compatible updates using its saved window.
  New opted-out policies are disabled at creation. The Mods tab waits for the
  current server's task list and preserves paused or disabled schedules during
  config-only saves and keep the scheduler's deadline until timing changes.
  An explicit opt-in change completes for the selected task across navigation.
  Staging alone never restarts a server.
- Backup, rollback, per-server serialization and apply-window semantics are
  exactly the staged-batch semantics already used for config and content.
- An MCP catalog change must not expose the review-and-apply flow as a tool;
  the fail-closed family gates enumerate mounted RPCs, so additive fields on
  an existing procedure avoid a new catalog/matrix/tool tuple.
- A policy can only stage compatible, unexcluded, not-already-staged updates;
  it cannot force an unknown or modified jar into the live tree. A policy that
  skips running servers is re-evaluated under the staging journal with a fresh
  server row, so a start during provider retry or download skips before the
  batch or any staged byte is written. Manual review does not consult that
  policy.
