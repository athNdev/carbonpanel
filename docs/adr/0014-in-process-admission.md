# ADR 0014: In-process start admission gate

Status: accepted

ADR 0011 shares the start capacity *check* between RPC and scheduler paths, but
the check is not a reservation. Two starts for servers on the same node could
both observe the same persisted running/starting total, pass admission and then
both start containers, overcommitting the node. This decision adds a gate around
that check without turning it into a distributed reservation system.

`internal/serverruntime.AdmissionCoordinator` is an instance-owned, cancellable
keyed mutex. The composition root constructs one coordinator and injects the
same instance into the RPC `ServerService`, the `Scheduler` and the
`DeepSleepManager`. Every production start or restart that calls
`serverruntime.CheckStartCapacity` is covered:

- RPC `StartServer`, `RestartServer` and `RecreateServer` acquire before the
  check and hold through container mutation and status persistence.
- Scheduler `executeStartTask`, `executeRestartTask` and `StartServerForBoot`
  do the same around their shared `checkNodeMemoryCapacity`.
- Background staged publication (`publishStagedBatch`) acquires when
  `plan.StartAfter` and holds through the eventual `Recreate`/`Start`.
- Deep-sleep wake holds the gate in `DeepSleepManager.WakeServer` from before
  `PrepareDeepSleepWake` (where the capacity check lives) through
  `StartContainer`, the bounded running-status wait and status persistence.
  `PrepareDeepSleepWake` itself does not acquire: it runs inside that gate as a
  `WakePreparer`, so it must not contend with its own holder.

Local aliases and case differences fold into the same key, so alias-based and
canonical callers contend with each other. Release is idempotent and refcounted,
different nodes proceed in parallel, and a cancelled or deadline-exceeded waiter
never enters the gate and never reaches Docker. The wake runs on a bounded
`wakeBootBudget` context in a detached goroutine; the gate is acquired and
released inside that single `WakeServer` call, never held by an unbounded
detached owner. Connect entrypoints map refusal and cancellation onto their
existing typed error codes; the scheduler and wake surface them as plain errors.

This is an **in-process gate for one controller process only**. It serializes
admission across the RPC and scheduler entrypoints in that process. It is not a
distributed lease and gives no cross-process atomic reservation: if two
controllers ever target the same node, the database's persisted running and
starting counts are still the source of truth at each check, and a second
controller is outside this gate. A process crash releases every held key, so no
lease can be stranded and no TTL or ownership token has to be reconciled.

A persistent lease with a TTL was rejected. A lease would have to define an
expiry, a renewal protocol, an owner identity and the outcome of a mutation
whose lease expired mid-flight. Those add crash, clock-skew and uncertain-outcome
failure modes to a check that already has a durable persisted state as its
authority. When the gate is absent the gate's own `Acquire` fails closed; the
service and scheduler constructors supply an instance-owned default so a
single-instance test or embedding does not silently drop the check, and the
composition root replaces it with the shared one. `DeepSleepManager` receives
the gate through a small local `AdmissionGate` interface so the `docker` package
does not import `serverruntime` (which already imports `docker`).

The gate does not own journal kinds, event emission, staged rollback or
`LastStarted`. Those remain with their existing owners. A refused or cancelled
start performs no Docker mutation, writes no journal row, emits no lifecycle
event and leaves `LastStarted` untouched; an admitted start keeps its existing
journal kind, event owner and staged rollback path. Real-store tests at the RPC
and scheduler seams cover same-node refusal, different-node parallelism,
cancellation while queued, those preserved outcomes, and gate coverage for boot
auto-start and staged publication with `StartAfter`. Deep-sleep wake tests
cover refusal before `StartContainer` and release after running status is saved.
Proxy route changes do not start containers, so they have no admission path to
gate.
