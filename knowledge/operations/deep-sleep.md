---
type: Runbook
title: Deep sleep and wake
description: Where hibernation and deep sleep are specified, sharing one idle-power module.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/docker/freezer.go
  - internal/docker/sleep.go
  - internal/docker/idle.go
  - docs/runbook-sleep-baseline.md
  - docs/deep-sleep-tier-b.md
sources:
  - title: Sleep baseline runbook
    resource: ../../docs/runbook-sleep-baseline.md
  - title: Deep sleep tier B
    resource: ../../docs/deep-sleep-tier-b.md
---

# Use this when

A server should hibernate or wake, or a sleep-related change is being reviewed.

# Procedure

Follow `docs/runbook-sleep-baseline.md` for the baseline and
`docs/deep-sleep-tier-b.md` for the tier-B behavior. Do not infer the state
machine from a single container inspect; read the recorded state.

# Known gap

Hibernation and deep sleep share `internal/docker/idle.go`. Each manager still
has a node resolver callback; freeze/wake must keep using Locator rather than
the controller socket. See [runtime seams](../architecture/runtime-seams.md)
and [node resolution](../architecture/node-resolution.md).

# Read next

- [Remote node fallback](../failures/remote-node-fallback.md)
- [Data safety](../invariants/data-safety.md)
