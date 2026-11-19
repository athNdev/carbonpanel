---
type: Architecture
title: Scheduler
description: Cron and lifecycle tasks run in the controller and surface progress over the WebSocket hub.
status: stable
owners: [oss-runtime]
code_refs: [internal/scheduler, internal/command, internal/events, internal/ws]
sources:
  - title: Cron validation issue
    resource: ../../docs/issues/002-SCH-7-cron-validation.md
  - title: Task recovery issue
    resource: ../../docs/issues/014-SCH-8-task-execution-recovery-ws-reconnect.md
---

# Parts

- [Scheduler — Shape + Rules](scheduler-shape.md) — The scheduler owns cron expressions and lifecycle actions such as start, stop,
- [Scheduler — How to check + Read next](scheduler-how-to-check.md) — Use the scheduler and task tests; for recovery behavior, use the fault harness

# Key facts

- On startup, recover or fail tasks that were interrupted by a restart. Do not
- The scheduler owns cron expressions and lifecycle actions such as start, stop,
- restart and backup. Long-running work is a task; task state and logs are
- A task execution left in the running state across a panel restart is marked
- failed. The recorded reason says to run the task again after the server is
- Boot auto-start and a scheduled start, stop, or restart refuse while a
- The sync releases it before a restart claims it again.
- Scheduled start and restart verify live host memory and the configured node
- Use the scheduler and task tests; for recovery behavior, use the fault harness
