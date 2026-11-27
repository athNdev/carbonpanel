---
type: Architecture
title: "Scheduler — Shape + Rules"
description: "The scheduler owns cron expressions and lifecycle actions such as start, stop,"
status: stable
owners: [oss-runtime]
code_refs: [internal/scheduler, internal/command, internal/events, internal/ws]
sources:
  - title: Parent concept
    resource: scheduler.md
---
# Shape

The scheduler owns cron expressions and lifecycle actions such as start, stop,
restart and backup. Long-running work is a task; task state and logs are
streamed through the WebSocket hub.

# Rules

- A scheduled action that destroys or replaces data must be explicitly
  configured and separately named. It is never implied by a cleanup rule.
- A failed task is recorded and visible; it must not be silently retried into a
  success state.
- On startup, recover or fail tasks that were interrupted by a restart. Do not
  leave them "running" forever.
- A task is not claimed when the running-execution check fails. The due run
  stays due, and a manual trigger returns that error. An unknown check is
  treated as running.
- A bare cron expression uses the task timezone; an explicit `TZ=` or `CRON_TZ=` prefix wins. A missed slot is not replayed: one due check runs the task once.
- A due slot is claimed only when schedule, deadline, status and policy revision match. Every due mod-update slot, whether one-shot, cron or interval, records its execution in the same guarded write as the slot; a one-shot also persists its terminal disabled state. An interruption before dispatch is recovered and an insert failure leaves the slot due. A cancellation that arrives before the worker registers its cancel function wins the handoff: the planner does not run and the cancelled outcome is kept.
- A refused claim still records a terminal outcome; the write has its own bounded context and retries after storage recovers, without redispatching the slot or clearing an active worker's record. A manual run whose RPC context was cancelled still records its terminal outcome instead of leaving the row running.
- An offline `RequireOnline` skip of a claimed run uses that same guarded, retried terminal write. A cancellation that won the handoff is kept, its next run is advanced as the skip path does, and the child chain does not run. A transient failure cannot leave the claim running. A chain-stop note updates only output, never a terminal status, and an orphan cancellation writes only while the row is still running.
- Retained legacy scheduled-config rows have no production cron consumer. They are pending manual apply/discard actions or an explicit operator conversion; do not re-enable cron execution silently.
- Only invalid schedule definitions disable tasks. Transient next-run persistence failures leave tasks enabled and due.
- When a chain stops at a step, that execution names the step and says the
  remaining steps were not run.
- A console command runs once. A configured retry is not repeated, because
  the command may already have been delivered.
- A scheduled backup holds the server lifecycle transition until the archive
  finishes. Restore and wake wait, and a backup refuses to start while that
  transition is already held.
- A scheduled backup writes a journal row before it pauses world saves or
  creates the archive. Each run uses an empty request id, so the row belongs
  to that run. A crash leaves the row active until startup recovery observes
  the server. Recovery says the archive was not finalized and that the backup
  can be run again. When that container is still running, save-on is sent
  before the lock is released, and a failed save-on keeps the lock. A stopped
  container is not started. The backup does not start when that server already
  has an active journal row.
- A task execution left in the running state across a panel restart is marked
  failed. The recorded reason says to run the task again after the server is
  idle.
- Boot auto-start and a scheduled start, stop, or restart refuse while a
  delete, restore, or migration journal row still owns that server.
- A scheduled stop and a live modpack file sync take that same transition.
  The sync releases it before a restart claims it again.
- Validate cron expressions before persisting them.
- Scheduled start and restart verify live host memory and the configured node
  limit before starting, stopping or recreating a container. Running/starting
  memory reservations are counted once; unknown capacity refuses the action.
