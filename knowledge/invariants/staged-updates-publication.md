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

Publishing a due batch is journaled. A future timestamp or an unfinished idle
stretch stays staged across an incidental restart. Idle does not fire unless
the same player-count provider used by freeze and deep sleep is installed, and
the time that stretch started is stored on the batch. A player joining clears
it. Deep sleep commits the batch onto the stopped server and does not start
the JVM. A running server is saved and restarted once. A frozen server gets a
real stop and start and is left running. If that publish fails, the previous
files and config are restored from the snapshot and the batch stays open. An
interrupted publication or an unprovable commit outcome also keeps the full
snapshot restore. Failed publications keep rows staged and reconcile
rolled-back batch configs; retries after completed publication only finish
bookkeeping. A live config field that moved after it was staged stops the
publish before anything is written. A reviewed mod candidate is validated
against the effective Docker `ServerConfig` type and version, and a runtime
config rollback is refused while such a candidate is still staged.

The publisher owns the server data path exclusively from before the undo
snapshot through publication and any rollback. Ordinary panel file writes,
uploads, deletes, renames, copies, archive creation, extraction and remote
archive download take a shared lease on the same path and are refused while a
publication owns it. A next-restart commit keeps that ownership in its returned
handle until the start/restart caller calls `Finish` or `Undo`. A reviewed
target's live digest is checked again immediately before its writer commits;
that guard is typed and bound to the target, but an out-of-band process is not
an atomic compare-and-swap. See ADR 0056.

A restart selects its file and config rows in one exclusive snapshot. When a
next-restart batch exists, only that batch plus legacy unbatched rows are
included; an at-time or idle batch is never published by an incidental restart.
Rows already applied after the last start are included so a safe failure can
return them to staged without applying them twice. A typed Docker preflight
failure before any container mutation compensates the snapshot even if the old
container is still running, but only when no durable applied evidence from an
earlier attempt exists. Applied content or config rows report that a replacement
may already embed the snapshot; that decision does not read the mutable server
status, because a wake can leave DEEPSLEEP after a verified compensating stop
and the reconciler can move a retained ERROR to STOPPED. Any other attempted
runtime outcome, including a transport timeout or an accepted replacement, is
retained: a stopped container is not proof that it is the old, unstaged one.
Config-only finalize never opens file recovery storage. Applied content rows are
durable cleanup evidence: a retry finishes that idempotent cleanup instead of
republishing, and cleanup runs before a cron-coexistence batch is parked so the
elapsed timed window can retry.

A modpack task can join that batch. The fetched tree is copied into the
staging directory and the live pack stays as it is until the window. The same
window commits the pack with any staged config, mods, and datapacks. A module
config or container-spec change can join the same batch. The live module row
stays as it is until the window, which recreates that module container when
the spec changed and still does not start a deep-sleeping server.

See [timed/idle publisher limits](../failures/staged-publication-timed-outcome.md) and [storage recovery](../../docs/storage-streaming.md).
