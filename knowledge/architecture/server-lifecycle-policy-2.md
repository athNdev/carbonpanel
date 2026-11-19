---
type: Architecture
title: "Server lifecycle policy — Policy (part 2)"
description: "scheduled staged publication retain the AutoCurseForge name-derived slug"
status: stable
owners: [oss-runtime]
code_refs: [internal/serverruntime, internal/rpc/services/server.go, internal/rpc/services/config.go, internal/scheduler/scheduler.go, internal/scheduler/staged_config.go, cmd/carbon-panel/main.go]
sources:
  - title: Parent concept
    resource: server-lifecycle.md
---
scheduled staged publication retain the AutoCurseForge name-derived slug
default when neither a slug nor page URL is explicit. Config-only recreation
retains its explicit source and does not infer one from the server name.
Scheduled staged publication resolves the server's node before any module or
container mutation. Plans that start a server also pass shared admission first;
an unavailable remote node never substitutes the controller daemon.
