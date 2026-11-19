---
type: Architecture
title: "Runtime model — Docker boundary"
description: "The controller holds a Docker client for the local socket or for a remote node."
status: stable
owners: [oss-runtime]
code_refs: [cmd/carbon-panel, internal/rpc, internal/proxy, internal/db, internal/docker, internal/docker/resource_enforcement.go, pkg/runtimeprofile, web/carbon-panel/src/lib/utils/resource-enforcement.ts, pkg/files, internal/scheduler, internal/operation, internal/reconciler, web/carbon-panel]
sources:
  - title: Parent concept
    resource: runtime.md
---
# Docker boundary

The controller holds a Docker client for the local socket or for a remote node.
Never assume it is local. Node-to-daemon resolution, lifecycle claims,
idle-power sweeps, proxy `PlanFor` and worldstore are the five seams in
[runtime seams](runtime-seams.md). See [node resolution](node-resolution.md).

A Minecraft server image is a tag on `itzg/minecraft-server`, or a full
reference to `itzg/minecraft-server` or `itzg/minecraft-bedrock-server`.
Other repositories are rejected. A tag stays a tag in the database so the
image picker still matches it. An image that is already on the daemon is
used without a registry check. Concurrent pulls of the same reference on
one client share one request, and a pull is success only after the daemon
reports that image present. A registry rate limit or an access denial is
named in the pull error, with the daemon message kept. The image ID of the container that last came
up stays recorded when a later pull or recreate fails. The registry repo
digest for that repository is stored beside the image ID and stays empty
when the daemon has no repo digest. A tag change is
written to the activity log with that ID, the repo digest, and whether the running
container moved to the new image. A cached image whose OS or CPU
architecture differs from the daemon is pulled once; if it still does
not match, creation stops before a container is made from it.
