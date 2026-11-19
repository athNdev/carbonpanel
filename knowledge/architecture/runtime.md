---
type: Architecture
title: Runtime model
description: Covers the API, console, proxy and scheduler. Effective container limits are qualified before create; storage-opt size does not bound a bind-mounted world. When storage.world_quota_mb is set and world bytes meet it, create is refused.
status: stable
owners: [oss-runtime]
code_refs: [cmd/carbon-panel, internal/rpc, internal/proxy, internal/db, internal/docker, internal/docker/resource_enforcement.go, pkg/runtimeprofile, web/carbon-panel/src/lib/utils/resource-enforcement.ts, pkg/files, internal/scheduler, internal/operation, internal/reconciler, web/carbon-panel]
sources:
  - title: Design specification
    resource: ../../DESIGN.md
---

# Parts

- [Runtime model — Shape + Listeners + Build boundary](runtime-shape.md) — cmd/carbon-panel/main.go loads configuration, opens the SQLite database,
- [Runtime model — Docker boundary](runtime-docker-boundary.md) — The controller holds a Docker client for the local socket or for a remote node.
- [Runtime model — Mount boundary + Work temp](runtime-mount-boundary.md) — The daemon interprets a bind source on the host, not inside the panel's
- [Runtime model — Runtime profile](runtime-runtime-profile.md) — Before the daemon is asked to create a container, docker.PreflightRuntime
- [Runtime model — Runtime profile (part 2)](runtime-runtime-profile-2.md) — The profile also reports the limits that are actually applied, not the ones
- [Runtime model — Runtime profile (part 3)](runtime-runtime-profile-3.md) — Server.resource_enforcement carries that view to the console: requested
- [Runtime model — How to check + Read next](runtime-how-to-check.md) — Detailed guidance on how to check.

# Key facts

- filesystem instead. `storage.world_quota_mb` is panel admission, not a daemon
- bind mount, so it is never quota-limited by the daemon: `storage-opt size`
- writable, and is reported as `storage_quota_is_not_a_world_quota` whenever it
- `world_disk_enforced` never from storage-opt, and the named warning codes.
- is treated as a hard cap. `storage.world_quota_mb` is shown as admission, not
- that were requested: memory and memory+swap, the process limit, the CPU cap,
- quota: when it is set and measured world bytes (`files.WorldBytes`, the same
- versus effective memory, `memory_enforced` only after `/info` qualification,
- `storage.data_dir`; `docker.TranslateToHostPath` then maps every data path
- `WritableLayerMB` are zero when Docker `/info` (`DaemonEnforcement`) says that
- that cannot take effect is a named warning (`memory_limit_unsupported`,
- reported as explicitly unknown, and neither is added to world bytes. The
- bytes are not a world quota and are not added to world bytes.
- Before container create, `DaemonEnforcement` reads Docker `/info` and the
- The Docker event supervisor refreshes its node set while it runs, including
- When storage.world_quota_mb is set and world bytes meet it, create is refused with world_quota_exceeded.
- bind. That directory is on the same disk as the world, it is executable, and
- Before the daemon is asked to create a container, `docker.PreflightRuntime`
- mount is the effective world bind. Preflight
- is set. World growth is measured by the metrics collector against the node
- refuses create with `world_quota_exceeded` before the daemon is asked. Unset
- `world_quota_unmeasured`. That refusal is not `WorldDiskEnforced` and is not
- Free bytes on the world filesystem are a separate budget beside total capacity
- unknown, and are not a hard world quota.
- daemons on cgroup v1, memory/swap/pids flags the daemon marks unsupported, and
- `Server.resource_enforcement` carries that view to the console: requested
- # Docker boundary
- The controller holds a Docker client for the local socket or for a remote node.
- The daemon interprets a bind source on the host, not inside the panel's
- container. When the panel runs in a container, `internal/hostmount` reads the
- Detection prefers the daemon's view of the panel's own container. A
- as-is. `CARBONPANEL_DATA_DIR` is not configuration: `storage.data_dir` is the
- daemon creates the same directory from the container bind, on the node that
- holds the world. `TMPDIR` and `-Djava.io.tmpdir` both point there. Scheduled
- world backups skip `.tmp`.
