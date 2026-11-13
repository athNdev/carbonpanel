---
type: Architecture
title: Server lifecycle admission
description: RPC and scheduled starts share node capacity admission before container mutation.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/serverruntime
  - internal/rpc/services/server.go
  - internal/scheduler/scheduler.go
sources:
  - title: Shared server admission
    resource: ../../docs/adr/0011-server-runtime-admission.md
---

# Policy

Start admission uses the smaller of the daemon memory and positive operator cap,
then combines persisted running/starting allocation with the target's memory.
Already running/starting targets count once. Paused and stopped targets need a
new active allocation. Admission neither mutates status nor updates LastStarted.

RPC translates typed refusals; the scheduler retains task and node resolution.
This check does not reserve capacity atomically across concurrent starts.
Staged recreation and rollback remain owned by their existing callers.
