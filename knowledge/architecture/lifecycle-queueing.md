---
type: Architecture
title: Lifecycle queueing and version gate
description: Rapid start/stop queueing, the interactive stop window, transition-end convergence and the version-change compatibility gate.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/rpc/services/server.go
  - internal/docker/stop.go
  - internal/docker/transition.go
  - internal/rpc/services/server_version_compat.go
sources:
  - title: Server lifecycle policy
    resource: server-lifecycle-policy.md
  - title: ADR 0055
    resource: ../../docs/adr/0055-version-change-compatibility-gate.md
---

# Queueing instead of failing

- An explicit start that arrives while a stop is still in flight waits for the
  stop (bounded by the stop window plus margin, logged while waiting) and then
  proceeds. Timeout is a typed Aborted error naming the unfinished stop.
- User-initiated stops use a shorter interactive stop window than internal
  paths, so the per-server transition lock is held for seconds, not minutes.
- A failed start on a dying container waits bounded for the old container to
  disappear before recreating; it never runs a second full stop window inside
  the lock.
- `docker.SetTransitionEndNotifier(reconciler.NotifyTransitionEnded)` ends the
  reconciler's blind window: stored status converges when a transition
  releases, not at the next sweep.

# Version-change gate

Before any mutation, a version change scans installed content for declared
compatibility with the target (fabric/quilt depends, plugin api-version,
datapack pack format). Incompatible files refuse the change with a per-file
list; unknown or unparseable content fails open. ADR 0055 is the contract.

# Read next

- [Server lifecycle policy](server-lifecycle-policy.md)
- [ADR 0055](../../docs/adr/0055-version-change-compatibility-gate.md)
