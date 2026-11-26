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
files and config are restored and the batch stays open. A refusal proven to
happen before a reviewed writer commits, such as a staged digest mismatch,
rolls back only targets that attempt mutated; unchanged live files are not
rewritten. An interrupted publication or an unprovable commit outcome keeps the
full snapshot restore. Failed publications keep rows staged and reconcile
rolled-back batch configs; retries after completed publication only finish
bookkeeping. A live config field that moved after it was staged stops the
publish before anything is written.

A modpack task can join that batch. The fetched tree is copied into the
staging directory and the live pack stays as it is until the window. The same
window commits the pack with any staged config, mods, and datapacks. A module
config or container-spec change can join the same batch. The live module row
stays as it is until the window, which recreates that module container when
the spec changed and still does not start a deep-sleeping server.

See [storage recovery](../../docs/storage-streaming.md).
