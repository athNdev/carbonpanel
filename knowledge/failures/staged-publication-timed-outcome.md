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
