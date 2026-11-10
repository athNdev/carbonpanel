---
type: Failure Mode
title: Known gaps
description: Cross-cutting gaps that are not defects to rush, with the live tracker as the authority.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/module/hooks.go
  - internal/docker/freezer.go
  - cmd/carbon-panel
  - internal/nodestorage
  - internal/docker/runtime_profile.go
  - pkg/files
  - internal/s3
sources:
  - title: Reliability plan
    resource: ../../docs/plans/reliability-hardening.md
---

# Current gaps

- A bind-mounted world is not quota-limited by Docker. `storage.container_writable_mb`
  caps only the container writable layer, and it applies only when the root is
  writable. `storage.world_quota_mb` is portable panel admission: when set,
  StartServer/create fails closed if measured world bytes meet or exceed it.
  That is not an xfs/project or volume quota and must not be reported as
  `WorldDiskEnforced`. World growth is still unbounded at the filesystem until
  a node filesystem or volume quota is present. The metrics collector
  reports separate budgets beside world bytes: free inodes on the world
  filesystem (or explicitly unknown, for example on Windows), and backup
  staging bytes under `storage.temp_dir` when configured (unknown when unset).
  The collector measures the node's Docker image store (deduplicated
  TotalSize) when a client or pool is configured. A missing client stays
  explicitly unknown. A daemon or lookup failure stays explicitly unknown.
  Image-cache bytes are not a world quota and are not added to world bytes.
  None of these is a hard world quota. Free bytes on the world filesystem are
  reported separately from total capacity; neither substitutes for xfs/project
  or volume quotas when a hard kernel world cap is required.

- Daemon `/info` qualification warns when cgroup or storage-opt limits may be
  ignored (for example rootless on cgroup v1) and zeros the matching effective
  limit fields. In-repo tests map `/info` payloads to those outcomes. Live
  confirmation that a target rootless or Desktop daemon actually honors or
  ignores cgroup and storage-opt flags remains OSS-247; the panel does not
  claim kernel enforcement from `/info` flags alone.

- `internal/module/hooks.go:execInModule` runs hook commands through the
  manager's own Docker client (`m.docker.Exec`), so a module hook does not
  follow the node its server runs on.
- `internal/docker/freezer.go` takes a `ContainerFreezer` seam, but
  `cmd/carbon-panel` wires it to the local client unconditionally.
- Remote nodes without a controller-side storage mount can use the
  `carbon-storage` JSON accessor worker when `storage.worker_image` is set
  (Docker one-shot attach). File RPCs that open `nodestorage.ForNode` use it
  (list/get/update/delete/rename, folder create, copy/move, uploads,
  InitFileDownload, latest-log, zip extract, CreateArchive,
  DownloadArchive, ExportWorld, CreateServer manual pack).
  Snapshot create/rollback and worldstore backup/restore also use that
  accessor. `DownloadRemoteArchive` and metrics still require shared
  storage or a further streaming worker path.

# How to use this

This concept is a map, not a backlog. The live tracker and
the reliability plan is authoritative for status. If a gap is fixed, update
this file in the same change.

# Read next

- [Remote node fallback](remote-node-fallback.md)
- [Persistence](../architecture/persistence.md)
