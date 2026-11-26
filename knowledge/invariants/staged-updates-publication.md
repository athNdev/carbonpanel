---
type: Invariant
title: "Staged server updates — Publication"
description: "A due staged batch publishes through the journal; recovery and retry semantics preserve live data."
status: stable
owners: [oss-runtime]
code_refs: [internal/db/staged_config.go, internal/db/staged_content.go, internal/staging, internal/scheduler/scheduler.go]
sources:
  - title: Parent concept
    resource: staged-updates.md
verified: { by: agent:opencode, at: 2026-11-27 }
---
# Rule

Publishing a due batch is journaled. A future timestamp or unfinished idle
stretch stays staged across an incidental restart. Idle fires only with the
freeze/deep-sleep player-count provider; the stretch start is stored on the
batch and a player joining clears it. Deep sleep commits onto the
stopped server without starting the JVM; a running server is saved and
restarted once; a frozen server is stopped, started and left running. A failed
publish restores the previous files and config and leaves the batch open;
interrupted or unprovable timed publications also restore the full snapshot.
Failed publications keep rows staged and reconcile rolled-back configs;
completed retries only finish bookkeeping. A live field changed after staging
stops the publish before any write. A reviewed mod candidate is validated
against the effective Docker `ServerConfig` type and version, and a runtime
config rollback is refused while one is still staged.
The publisher owns the server data path exclusively from before the undo
snapshot through publication and rollback; ordinary writes and archive
operations take a shared lease and are refused meanwhile. A next-restart commit
keeps that ownership until `Finish` or `Undo`. A reviewed target's live digest
is rechecked immediately before its writer commits; an out-of-band process
remains outside the typed guard. See ADR 0056.

A restart selects its file and config rows in one exclusive snapshot. When a
next-restart batch exists, only that batch plus legacy unbatched rows (zero
string or SQL NULL) are included; at-time and idle batches are excluded. The
commit durably claims admission before copying files or applying config, so DB
config staging cannot join the recovery tree; full compensation or a no-op
releases the claim, and an unapplied recovery tree is restored from its
manifest before reuse. A failure before this attempt mutated the runtime, or a
typed Docker preflight, compensates only the files and config this attempt
applied. Rows applied before the snapshot stay applied because a prior
replacement may embed them; that decision reads durable rows, not mutable
status (a wake can leave DEEPSLEEP and the reconciler can turn a retained ERROR
into STOPPED). Any other attempted runtime outcome is retained in full and
sealed against new admission. An acknowledgement records that the runtime
accepted it; a replacement whose canonical ID could not be persisted is sealed
but not acknowledged, and a retry reconciles the canonical container before
starting or recreating. A failed acknowledgement seals and runs no cleanup or
close. Retries finish cleanup without recreating; unacknowledged retained
batches still owe the runtime, while acknowledged owned config rows with an old
`LastStarted` do not recreate; newly applied and legacy rows do.
Persisted mixed rows resolve by discarding the newly staged rows and retrying
the retained snapshot; recovery is never overwritten or deleted to make room.
Applied content rows are durable cleanup evidence: retry the idempotent cleanup
instead of republishing, and clean up before parking a cron-coexistence batch.

A modpack task or module spec change can join the batch; a module recreate
still does not start a deep-sleeping server. See [timed/idle publisher limits](../failures/staged-publication-timed-outcome.md) and [storage recovery](../../docs/storage-streaming.md).
