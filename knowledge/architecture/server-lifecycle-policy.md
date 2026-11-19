---
type: Architecture
title: "Server lifecycle policy — Policy"
description: "Start admission uses the smaller of the daemon memory and positive operator cap,"
status: stable
owners: [oss-runtime]
code_refs: [internal/serverruntime, internal/rpc/services/server.go, internal/rpc/services/config.go, internal/scheduler/scheduler.go, internal/scheduler/staged_config.go, cmd/carbon-panel/main.go]
sources:
  - title: Parent concept
    resource: server-lifecycle.md
---
# Policy

Start admission uses the smaller of the daemon memory and positive operator cap,
then combines persisted running/starting allocation with the target's memory.
Already running/starting targets count once. Paused and stopped targets need a
new active allocation. Admission neither mutates status nor updates LastStarted.

RPC translates typed refusals; the scheduler retains task and node resolution.
The check itself does not reserve capacity. A shared instance-owned
`serverruntime.AdmissionCoordinator` gates every production start/restart by
canonical node key from before that check through container mutation and status
persistence, so same-node starts cannot both admit against the same persisted
total; different nodes proceed in parallel. Covered paths are RPC start,
restart and recreate; scheduler start, restart and boot auto-start; staged
publication with `StartAfter`; deep-sleep wake (where `WakeServer` holds the
gate across the wake preparer, `StartContainer`, the bounded running wait and
status persistence, and the preparer itself must not re-acquire); both migration
node starts (target container and source rollback, acquired in sorted key
order); config and routing recreation; reconciler self-heal;
the scheduler's post-backup restart; and snapshot-rollback restart. Those last
three restore a server the database already counts as running/starting, but are
gated anyway so no container mutation that can change that total escapes the
same keyed window. `DeepSleepManager`'s constructor installs a fail-closed gate
and refuses to wake until the composition root wires the shared coordinator;
wiring nil cannot remove it. It is an in-process gate for one controller
process, not a distributed lease: a crash releases it and a persistent lease
with TTL, renewal and uncertain outcomes was rejected (ADR 0014). Persisted
running and starting counts remain the source of truth.
Cancelled waiters never reach Docker; the gate owns no journal kinds, events,
staged rollback or LastStarted.
Server/config/routing RPC and scheduled staged recreation share canonical identity persistence
through `serverruntime.Recreate`. The Docker result can identify the retained
original, a verified replacement or a partial replacement. Errors record that
identity and an error status without advancing LastStarted; missing identities
are failures. A bounded recovery context saves the observed outcome even after
request cancellation. Docker and persistence failures are both returned.
Success status, explicit start intent, staged rollback, journals and events
remain owned by the existing callers.
Routing recreation returns a failure when Docker cannot apply its new spec,
and retains deep sleep when a successful replacement stays stopped. Staged
start/restart retain their applied recovery marker when Docker succeeds but
identity persistence fails; they do not roll back only the database.
Explicit starts share failure persistence without advancing LastStarted.
Restart fallback starts a stopped replacement before recording success; a
failed start or state save returns failure without a restart event. Primary
restart and fallback failure details are retained together.
Runtime config preparation inherits only missing provider credentials, using
one best-effort global lookup without persisting inherited secrets. Launch and
