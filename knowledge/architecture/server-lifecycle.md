---
type: Architecture
title: Server lifecycle policy
description: Starts share node admission behind an in-process node gate, and config recreation records canonical container recovery identities.
status: stable
owners: [oss-runtime]
code_refs: [internal/serverruntime, internal/rpc/services/server.go, internal/rpc/services/config.go, internal/scheduler/scheduler.go, internal/scheduler/staged_config.go, cmd/carbon-panel/main.go]
sources:
  - title: Shared server admission
    resource: ../../docs/adr/0011-server-runtime-admission.md
  - title: In-process start admission gate
    resource: ../../docs/adr/0014-in-process-admission.md
---

# Parts

- [Server lifecycle policy — Policy](server-lifecycle-policy.md) — Start admission uses the smaller of the daemon memory and positive operator cap,
- [Server lifecycle policy — Policy (part 2)](server-lifecycle-policy-2.md) — scheduled staged publication retain the AutoCurseForge name-derived slug
