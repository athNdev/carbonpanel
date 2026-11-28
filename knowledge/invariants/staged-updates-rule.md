---
type: Invariant
title: "Staged server updates — Rule"
description: "A server has one open update batch. Staging adds to that batch. It must not"
status: stable
owners: [oss-runtime]
code_refs: [internal/db/staged_config.go, internal/db/staged_content.go, internal/rpc/services/stage_content.go, internal/staging, internal/rpc/services/update_batch.go, internal/scheduler/scheduler.go, web/carbon-panel/src/lib/components/server-configuration.svelte, web/carbon-panel/src/lib/components/server-tasks.svelte]
verified: { by: agent:opencode, at: 2026-11-28 }
sources:
  - title: Parent concept
    resource: staged-updates.md
---
# Rule

A server has one open update batch. Staging adds to that batch. It must not
call Docker, change server status, write the live `ServerConfig` row, or write
the mods directory, the datapacks directory, or the world.

One batch may hold at most one staged content row per normalized live target
after expansion (case, separator and trailing-separator aliases name the same
file). A modpack row stores the literal target `modpack` but expands to its pack
files, so a modpack and a separate file or reviewed-mod row cannot share a batch;
admission refuses that mix and publication expands the full pending set and
refuses overlap before recording APPLYING. A second edit to one module in the
same batch is refused with an actionable conflict: discard the earlier module
item before staging another. Every such refusal rolls back the new row, because
a single live post-image cannot verify two recorded entries during recovery.

File bytes for a staged install or replace go through node storage at
`.carbon-panel/staged/<id>/` inside the server data root. A delete records the
live path and removes nothing. Discard deletes those staged bytes. A remote
node whose storage is unreachable refuses the stage and gets no
controller-local copy.

Binary staging and publication use bounded streams. Before any live file
mutation, old contents and a versioned SHA-256 manifest are committed under
`.carbon-panel/undo/<batch-id>/` on the owning node. Rollback verifies these
copies before promoting them; failed rollback/interruption retains recovery
and prevents a fresh apply from overwriting it. Successful rollback or finalized
publication cleans only that batch's undo directory. See
[storage recovery](../../docs/storage-streaming.md).
Staging validation and direct commit refuse targets inside the undo subtree,
including case and separator aliases, so publication cannot overwrite its own
recovery manifest or backup files.

Modpack expansion skips and refuses controller state: the clone cache and its
staged and snapshot siblings, the legacy pending marker inside the cache, and
the live pending marker kept beside the cache (the cache directory name joined
with the marker file name). A pack therefore cannot publish over the scheduler's
pending-update state.

Client-only mod jars and archives that are not datapacks are rejected before any staged byte is kept. A completed upload session for a staged datapack refused with `Aborted` or `FailedPrecondition` stays retryable; other datapack staging failures clean it up.

Publication, recovery, retry and modpack semantics are in
[Staged server updates — Publication](staged-updates-publication.md).

The server configuration screen reads that open batch. From there an admin
chooses next restart, a UTC timestamp, or idle minutes, and can discard one
item or the whole batch. Apply now still restarts the container.
