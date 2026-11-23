---
type: Runbook
title: "Deep sleep and wake — Use this when + Procedure + Player knock versus intentional stop + Known gap + Read next"
description: "A server should hibernate or wake, or a sleep-related change is being reviewed."
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: deep-sleep.md
---
# Use this when

A server should hibernate or wake, or a sleep-related change is being reviewed.

# Procedure

Follow `docs/runbook-sleep-baseline.md` for the baseline and
`docs/deep-sleep-tier-b.md` for the tier-B behavior. Do not infer the state
machine from a single container inspect; read the recorded state.

# Player knock versus intentional stop

A player knock must not start an intentionally stopped server. `WakeServer`
starts only recorded deep sleep. AutoDeepSleep remaining enabled after an
operator stop is not a wake license. Freeze wake refuses a non-paused status.
The reconciler does not recreate a stopped container as a substitute wake.

`WakeServer` holds the shared in-process node admission gate from before the
wake preparer through `StartContainer` and running-status persistence, so a cold
wake cannot overcommit a node against another start (ADR 0014). The preparer
(`PrepareDeepSleepWake`) runs inside that gate and does not acquire it again.
The gate is mandatory and fail closed: the constructor installs a gate that
refuses every wake until the composition root wires the shared coordinator, and
wiring a nil coordinator cannot remove it. Only an explicitly named test-only
opt-out clears it. The wake runs on a bounded background budget; the gate is
released inside the single `WakeServer` call, never held by an unbounded detached
owner.

# Manual operator modes

`PauseServer` and `DeepSleepServer` are the operator-driven entries into the
same two managers the idle policies use: `PauseServer` freezes a running
container with the cgroup freezer (`HibernateServer`), `DeepSleepServer` parks
it with `SleepServer`. Both refuse a server that is not running, both refuse
while another lifecycle operation is journaled for that server, and both fail
closed with `UNAVAILABLE` when the composition root wired no manager. Both are
authorized as `servers/stop` scoped to the server id, so a scoped role may park
only its own server. `StartServer` unwakes a frozen or deeply asleep server.

# Known gap

Hibernation and deep sleep share `internal/docker/idle.go`. Each manager still
has a node resolver callback; freeze/wake must keep using Locator rather than
the controller socket. See [runtime seams](../architecture/runtime-seams.md)
and [node resolution](../architecture/node-resolution.md).

# Read next

- [Remote node fallback](../failures/remote-node-fallback.md)
- [Data safety](../invariants/data-safety.md)
