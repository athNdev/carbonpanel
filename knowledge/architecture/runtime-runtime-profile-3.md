---
type: Architecture
title: "Runtime model — Runtime profile (part 3)"
description: "Server.resource_enforcement carries that view to the console: requested"
status: stable
owners: [oss-runtime]
code_refs: [cmd/carbon-panel, internal/rpc, internal/proxy, internal/db, internal/docker, internal/docker/resource_enforcement.go, pkg/runtimeprofile, web/carbon-panel/src/lib/utils/resource-enforcement.ts, pkg/files, internal/scheduler, internal/operation, internal/reconciler, web/carbon-panel]
sources:
  - title: Parent concept
    resource: runtime.md
---
`Server.resource_enforcement` carries that view to the console: requested
versus effective memory, `memory_enforced` only after `/info` qualification,
`world_disk_enforced` never from storage-opt, and the named warning codes.
The OSS console labels RAM as configured, advisory, or enforced from those
codes (`memoryLimitCopy` / `worldDiskCopy`); frontend tests fail if a warning
is treated as a hard cap. `storage.world_quota_mb` is shown as admission, not
kernel enforcement. OOM versus forced-stop stay distinct via `status_reason`
(`exitFailureCopy`).
