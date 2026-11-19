---
type: Architecture
title: "Runtime model — Runtime profile (part 2)"
description: "The profile also reports the limits that are actually applied, not the ones"
status: stable
owners: [oss-runtime]
code_refs: [cmd/carbon-panel, internal/rpc, internal/proxy, internal/db, internal/docker, internal/docker/resource_enforcement.go, pkg/runtimeprofile, web/carbon-panel/src/lib/utils/resource-enforcement.ts, pkg/files, internal/scheduler, internal/operation, internal/reconciler, web/carbon-panel]
sources:
  - title: Parent concept
    resource: runtime.md
---
The profile also reports the limits that are actually applied, not the ones
that were requested: memory and memory+swap, the process limit, the CPU cap,
and the writable-layer cap. `ContainerLimits` keeps the HostConfig request on
`Requested*` fields. Effective `MemoryBytes`, `PidsLimit`, and
`WritableLayerMB` are zero when Docker `/info` (`DaemonEnforcement`) says that
cap is unsupported, or when a rootless daemon is not on cgroup v2. A request
that cannot take effect is a named warning (`memory_limit_unsupported`,
`swap_limit_unsupported`, `pids_limit_unsupported`,
`writable_layer_cap_unsupported`, `rootless_cgroup_unqualified`) and is never
reported as `*_unset`. The in-repo table
`TestEffectiveLimitsMatchRequestVersusDaemonInfo` encodes those request versus
`/info` outcomes without a live game container. Live confirmation that a given
rootless or Desktop daemon actually honors (or ignores) those flags remains
OSS-247. The world is a
bind mount, so it is never quota-limited by the daemon: `storage-opt size`
sizes only the container writable layer, is applied only when the root is
writable, and is reported as `storage_quota_is_not_a_world_quota` whenever it
is set. World growth is measured by the metrics collector against the node
filesystem instead. `storage.world_quota_mb` is panel admission, not a daemon
quota: when it is set and measured world bytes (`files.WorldBytes`, the same
total as the collector's `WorldSize`) meet or exceed it, `PreflightRuntime`
refuses create with `world_quota_exceeded` before the daemon is asked. Unset
(0) never refuses. A set quota that cannot be measured fails closed as
`world_quota_unmeasured`. That refusal is not `WorldDiskEnforced` and is not
an xfs/project or volume quota. Free inodes on that filesystem and backup staging under
`storage.temp_dir` are separate budgets: each is measured when possible or
reported as explicitly unknown, and neither is added to world bytes. The
collector measures the node's Docker image store (deduplicated TotalSize)
when a client or pool is configured. A missing client stays explicitly
unknown. A daemon or lookup failure stays explicitly unknown. Image-cache
bytes are not a world quota and are not added to world bytes.
Free bytes on the world filesystem are a separate budget beside total capacity
(`DiskTotal`); they are measured when possible or reported as explicitly
unknown, and are not a hard world quota.

Before container create, `DaemonEnforcement` reads Docker `/info` and the
runtime profile compares the resolved request to those capabilities. Rootless
daemons on cgroup v1, memory/swap/pids flags the daemon marks unsupported, and
writable-layer caps on storage stacks without overlay2+xfs+pquota each produce
named warnings so an ignored limit is not mistaken for enforcement.

