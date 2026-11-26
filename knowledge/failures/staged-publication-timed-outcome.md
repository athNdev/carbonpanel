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
verified: { by: agent:opencode, at: 2026-11-27 }
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
for the next-restart snapshot path. The next-restart path now has a durable
batch-bound phase record (`server_update_publications`) that separates the
prepared snapshot, the applying marker, the dispatch intent, the replacement
identity captured at creation, the verified outcome and cleanup. A timed/idle
ambiguous outcome or a crash in that config-only window still requires operator
review; the panel does not report it as a safe retry. `ApplyBatch` does not
write the phase record, so no timed/idle acknowledgement or resumption is
claimed here. Closing this gap needs the same phases around `ApplyBatch`'s
publisher plus an observed container identity that a retry can verify.

The next seam for timed/idle publication is to write the same record around its
publisher: `prepared` once files, config and module rows are on the live tree;
`applying` before the first live write; `dispatched` with the exact applied row
ids immediately before the Docker call; and `verified` only after a positive
runtime observation. A retry at `prepared`/`applying` must reuse or restore the
recorded snapshot without recopying files or reapplying modules. A retry at
`dispatched` must observe the recorded replacement identity instead of rolling
back or closing the batch, and an unrecorded or unrelated identity stays sealed.
A module mutation needs durable module intent first, exactly as in the
next-restart path. Until that wiring and its crash-window tests exist, timed/idle
publication must not claim the next-restart recovery semantics.

Required tests before claiming scheduled acceptance: a crash between each phase
transition, an ambiguous publisher error that leaves `publishing`, a config-only
crash after apply but before publish, a mixed content-plus-config resume, and
startup recovery settling an unknown operation against an observed container.
Until those exist, scheduled timed/idle publication is not a safe automatic
retry path.
