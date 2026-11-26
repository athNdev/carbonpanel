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
verified: { by: agent:opencode, at: 2026-11-26 }
---
# Rule

Publishing a due batch is journaled. A future timestamp or unfinished idle
stretch stays staged across an incidental restart. Idle fires only with the
freeze/deep-sleep player-count provider installed; the stretch start is stored
on the batch and a player joining clears it. Deep sleep commits onto the
stopped server without starting the JVM; a running server is saved and
restarted once; a frozen server is stopped, started and left running. If that
publish fails, the previous files and config are restored and the batch stays
open. Interrupted or unprovable timed publications also restore the full
snapshot. Failed publications keep rows staged and reconcile rolled-back batch
configs; retries after completed publication only finish bookkeeping. A live
field changed after staging stops the publish before any write. A reviewed mod
candidate is validated against the effective Docker `ServerConfig` type and
version, and a runtime config rollback is refused while one is still staged.

The publisher owns the server data path exclusively from before the undo
snapshot through publication and rollback; ordinary writes and archive
operations take a shared lease and are refused meanwhile. A next-restart commit
keeps that ownership until the caller calls `Finish` or `Undo`. A reviewed
target's live digest is rechecked immediately before its writer commits; the
typed guard is bound to the target, but an out-of-band process is not an atomic
compare-and-swap. See ADR 0056.

A restart selects its file and config rows in one exclusive snapshot. When a
next-restart batch exists, only that batch plus legacy unbatched rows (zero
string or SQL NULL) are included; at-time and idle batches are excluded. A
failure before this attempt mutated the runtime, or a typed Docker preflight,
compensates only the files and config this attempt applied. Rows applied before
the snapshot stay applied because a prior replacement may embed them; that
decision reads durable rows, not mutable status (a wake can leave DEEPSLEEP and
the reconciler can turn a retained ERROR into STOPPED). Any other attempted
runtime outcome, including a timeout or accepted replacement, is retained in
full. A retained publication seals the batch against new content or config
admission, and an acknowledged publication records that the runtime accepted
it. Retry then finishes cleanup without recreating; an unacknowledged retained
batch still owes the runtime. Persisted mixed rows resolve by discarding the
newly staged rows and retrying the retained snapshot; recovery is never
overwritten or deleted to make room. Config-only finalize never opens file
recovery storage. Applied content rows are durable cleanup evidence: retry the
idempotent cleanup instead of republishing, and clean up before parking a
cron-coexistence batch.

A modpack task or a module config/container-spec change can join the same
batch. Its fetched tree and live module row wait for the window; a module
recreate still does not start a deep-sleeping server.

See [timed/idle publisher limits](../failures/staged-publication-timed-outcome.md) and [storage recovery](../../docs/storage-streaming.md).
