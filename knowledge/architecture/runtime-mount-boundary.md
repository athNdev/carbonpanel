---
type: Architecture
title: "Runtime model — Mount boundary + Work temp"
description: "The daemon interprets a bind source on the host, not inside the panel's"
status: stable
owners: [oss-runtime]
code_refs: [cmd/carbon-panel, internal/rpc, internal/proxy, internal/db, internal/docker, internal/docker/resource_enforcement.go, pkg/runtimeprofile, web/carbon-panel/src/lib/utils/resource-enforcement.ts, pkg/files, internal/scheduler, internal/operation, internal/reconciler, web/carbon-panel]
sources:
  - title: Parent concept
    resource: runtime.md
---
# Mount boundary

The daemon interprets a bind source on the host, not inside the panel's
container. When the panel runs in a container, `internal/hostmount` reads the
mount table of its own container at startup and derives the host path behind
`storage.data_dir`; `docker.TranslateToHostPath` then maps every data path
onto it. No environment variable restates the mapping, because a duplicate that
can disagree with the compose file is a data-safety bug. A relative host source
is refused (`mount path should be absolute`).

Startup refuses the mapping rather than degrading:

- no volume covering the data directory (`ErrNotMounted`) — worlds would be
  written into the container layer and lost on recreate;
- a read-only mount or a directory the process cannot write (probe file) —
  `no write permission on the mount dir`;
- a tmpfs data directory, which is discarded with the container.

Detection prefers the daemon's view of the panel's own container. A
host-networked container has no container to inspect — its hostname is the
machine name — so the panel also reads the kernel mount table, where the source
path of a bind mount is recorded. That path is the host directory. Detection is
best-effort so a restricted Docker socket proxy does not stop the panel
booting. When the mount table cannot be read, the deprecated
`CARBONPANEL_HOST_DATA_PATH` is used if set, and otherwise paths are used
as-is. `CARBONPANEL_DATA_DIR` is not configuration: `storage.data_dir` is the
source of truth.

A remote module bind uses its server-relative path even when the controller
and daemon share the same absolute storage root. Preparing a bind directory
requires a reachable controller mount and confines creation to that server.

# Work temp

`/tmp` stays a 64 MiB noexec tmpfs. Modpack downloads, archive extracts, and
native libraries such as Netty and c2me use `/data/.tmp` on the server data
bind. That directory is on the same disk as the world, it is executable, and
the panel creates it mode 01777 when it can see the data path — including
through a controller mount of a remote node's shared storage. A remote daemon
whose storage the panel cannot see creates the same directory from the
container bind, on the node that holds the world. `TMPDIR` and
`-Djava.io.tmpdir` both point there. Scheduled world backups skip `.tmp`.
