---
type: Decision
title: Resolve an unpersisted replacement identity through an explicit marker
description: A created replacement whose identity write fails is retried, then recorded as identity-unresolved; a retry may adopt that dispatch's canonical container and verify it, never the baseline or an unrelated discovery.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/db/staged_publication.go
  - internal/staging/apply.go
  - internal/staging/restart_outcome.go
  - internal/rpc/services/server.go
  - internal/scheduler/scheduler.go
sources:
  - title: Publication invariant
    resource: ../invariants/staged-updates-publication.md
  - title: Publication phase record ADR
    resource: ../../docs/adr/0058-staged-publication-phase-record.md
---

# Choice

The replacement identity returned at creation is written with a bounded context
detached from caller cancellation and a short idempotent retry: repeating the
same identity cannot duplicate a mutation. If the write still fails, the record
moves from `dispatched` to `identity-unresolved` instead of staying dispatched
with an empty expected identity.

A retry at `identity-unresolved` may adopt the canonical container the dispatch
created as the expected identity, then verify it exactly like a recorded
dispatch. Adoption requires the durable marker, a non-empty observed container
and a container different from the recorded baseline. It never turns ordinary
container discovery or the baseline into publication evidence, and an in-place
dispatch with no replacement remains sealed.

If even the marker cannot be written, the returned error names the batch and
requires operator inspection. The runtime may already contain the batch, so
nothing is compensated or acknowledged while the identity is unknown.

# Consequences

- A transient identity write failure no longer leaves an unrecoverable
  dispatched record that refuses every retry.
- A durable marker is required before discovery can be adopted; a process that
  never recorded it cannot silently acknowledge an unrelated container.
- Generic compensation (`AbandonUpdatePublication`) cannot erase a dispatch.
  Only the explicit typed-preflight transition may release one, and only when
  every attempt proved no container mutation began.
