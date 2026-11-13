# ADR 0011: Share server start admission

Status: accepted

RPC and scheduler lifecycle paths must apply the same node memory limits before
mutating a container. Maintaining separate calculations previously allowed their
policies to diverge.

`internal/serverruntime.CheckStartCapacity` owns node lookup, host/operator caps,
persisted active memory and counting the target reservation once. It accepts the
real database store and a host-memory callback. RPC owns Connect error mapping;
the scheduler owns task scheduling and node Docker resolution. Typed refusals
keep transport presentation outside the shared policy.

This extraction preserves the existing running/starting accounting and performs
no state mutation. It is an admission check rather than an atomic capacity
reservation; concurrent starts require a separately designed reservation policy.
Config RPC and scheduled staged recreation also share `serverruntime.Recreate`
for canonical container identity persistence. Docker owns reversible replacement;
the shared policy records its result before callers proceed. It records partial
failure without advancing LastStarted, and returns persistence failures alongside
Docker failures. Saving an observed identity uses a separate five-second context
because request cancellation cannot undo an external daemon mutation. Success
status, explicit start intent, staged rollback, journal kinds and event ownership
remain in their existing owners until separately characterized and migrated.

Real SQLite tests cover aggregate admission and unchanged lifecycle state. The
scheduler integration tests retain actual Docker HTTP interactions and verify
that refused starts/restarts perform no container mutation.
