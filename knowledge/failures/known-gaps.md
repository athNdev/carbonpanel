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
  - internal/s3
sources:
  - title: Reliability plan
    resource: ../../docs/plans/reliability-hardening.md
---

# Current gaps

- A bind-mounted world is not quota-limited. `storage.container_writable_mb`
  caps only the container writable layer, and it applies only when the root is
  writable. World growth is measured and alerted, not capped: a node filesystem
  or volume quota is the only hard bound available today. The metrics collector
  reports separate budgets beside world bytes: free inodes on the world
  filesystem (or explicitly unknown, for example on Windows), and backup
  staging bytes under `storage.temp_dir` when configured (unknown when unset).
  The collector measures the node's Docker image store (deduplicated
  TotalSize) when a client or pool is configured. A missing client stays
  explicitly unknown. A daemon or lookup failure stays explicitly unknown.
  Image-cache bytes are not a world quota and are not added to world bytes.
  None of these is a hard world quota. Free bytes on the world filesystem are
  reported separately from total capacity; neither substitutes for xfs/project
  or volume quotas when a hard world cap is required.

- Daemon `/info` qualification warns when cgroup or storage-opt limits may be
  ignored (for example rootless on cgroup v1). Live confirmation on the target
  daemon profile remains in OSS-247; the panel does not claim enforcement
  without capability evidence.

- `internal/module/hooks.go:execInModule` runs hook commands through the
  manager's own Docker client (`m.docker.Exec`), so a module hook does not
  follow the node its server runs on.
- `internal/docker/freezer.go` takes a `ContainerFreezer` seam, but
  `cmd/carbon-panel` wires it to the local client unconditionally.
- Remote nodes without a controller-side storage mount can use the
  `carbon-storage` JSON accessor worker when `storage.worker_image` is set
  (Docker one-shot attach). File RPCs that open `nodestorage.ForNode` use it
  (list/get/update/delete/rename, folder create, copy/move, uploads,
  InitFileDownload, latest-log, zip extract). Creating or downloading
  archives, world export, backup/worldstore and metrics still require shared
  storage or a streaming write/worker path.

# How to use this

This concept is a map, not a backlog. The live tracker and
the reliability plan is authoritative for status. If a gap is fixed, update
this file in the same change.

# Read next

- [Remote node fallback](remote-node-fallback.md)
- [Persistence](../architecture/persistence.md)
