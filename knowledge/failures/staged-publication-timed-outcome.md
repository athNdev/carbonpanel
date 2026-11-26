---
type: Failure Mode
title: "Timed/idle staged publication — ambiguous runtime outcome"
description: "The timed/idle publisher records every error as definite and cannot distinguish a crash before its container mutation from one after it."
status: stable
owners: [oss-runtime]
code_refs: [internal/staging/apply.go, internal/operation, internal/scheduler/scheduler.go]
sources:
  - title: Publication invariant
    resource: ../invariants/staged-updates-publication.md
verified: { by: agent:opencode, at: 2026-11-26 }
---
# Gap

`ApplyBatch` publishes timed and idle batches through `operation.RunJournaled`.
That journal treats every publisher error as a definite failure, so it frees the
server lock and lets the caller restore the undo snapshot. A Docker transport
timeout can be ambiguous: the daemon may already have accepted the restart even
though the panel saw an error. Restoring the snapshot then leaves a running
container on a spec the staged rows no longer describe.

A crash after a config-only timed batch has been applied but before its publish
is also indistinguishable from a completed publish. On retry the batch has no
staged row left, so finalization can close it without repeating the container
mutation. Content rows avoid this particular window because they stay staged
until after the publish. Durable operation-journal recovery observes the
container and releases its lock; it does not settle this staged phase.

# Limit

Automatic compensation is only claimed for a typed Docker preflight failure and
for the next-restart snapshot path. A timed/idle ambiguous outcome or a crash in
that config-only window requires operator review; the panel does not report it
as a safe retry. Closing this gap needs a durable publish-phase marker (or an
equivalent journal outcome) that both the panel and startup recovery can read.

The bounded next design is a persisted phase on the open batch:
`prepared` once files, config, and module rows are on the live tree;
`publishing` immediately before the container mutation; and `published` after
it succeeds. A retry at `prepared` must resume the publish without copying
files, reapplying config, or recreating modules again. A retry at `publishing`
must treat the container outcome as unknown and settle it from an observation,
not roll the snapshot back or close the batch. `published` only finishes marks,
cleanup, and close. `prepared` is also what tells a config-only batch that its
publish is owed; content rows alone cannot prove that.

Required tests before claiming scheduled acceptance: a crash between each phase
transition, an ambiguous publisher error that leaves `publishing`, a config-only
crash after apply but before publish, a mixed content-plus-config resume, and
startup recovery settling an unknown operation against an observed container.
Until those exist, scheduled timed/idle publication is not a safe automatic
retry path.
