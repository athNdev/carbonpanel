---
type: Architecture
title: Server lifecycle policy
description: Starts share node admission and config recreation records canonical container recovery identities.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/serverruntime
  - internal/rpc/services/server.go
  - internal/rpc/services/config.go
  - internal/scheduler/scheduler.go
  - internal/scheduler/staged_config.go
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
scheduled staged publication retain the AutoCurseForge name-derived slug
default when neither a slug nor page URL is explicit. Config-only recreation
retains its explicit source and does not infer one from the server name.
Scheduled staged publication resolves the server's node before any module or
container mutation. Plans that start a server also pass shared admission first;
an unavailable remote node never substitutes the controller daemon.
