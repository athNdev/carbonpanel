---
type: Architecture
title: Runtime model
description: The daemon owns the API, console, proxy, scheduler and SQLite, and starts them from cmd/carbon-panel.
status: stable
owners: [oss-runtime]
code_refs:
  - cmd/carbon-panel
  - internal/rpc
  - internal/proxy
  - internal/db
  - internal/docker
  - internal/scheduler
  - internal/operation
  - internal/reconciler
  - web/carbon-panel
sources:
  - title: Design specification
    resource: ../../DESIGN.md
---

# Shape

`cmd/carbon-panel/main.go` loads configuration, opens the SQLite database,
registers the RPC services, starts the TCP proxy and starts the scheduler,
reconciler and WebSocket hub. The console is a SvelteKit build embedded into the
binary. Interrupted server mutations are journaled and recovered on startup.
The Docker event supervisor refreshes its node set while it runs, including
each node's endpoint generation. A changed generation replaces that node's
watcher. A node whose client cannot
be resolved is stale and does not stop the others. A full event buffer drops
the event instead of stalling that node's stream. The periodic sweep reads
the container again, so a dropped or reordered event does not leave status,
the proxy route, or log subscriptions stuck.

# Listeners

- `:8080` — Connect-RPC, HTTP and the WebSocket hub.
- `:25565` — the public TCP proxy. See [proxy](proxy.md).

# Build boundary

The backend tests expect `web/carbon-panel/build` to exist; CI creates a
placeholder when the frontend is not built. Production builds run
`bash scripts/web-build.sh` first.

# Docker boundary

The controller holds a Docker client for the local socket or for a remote node.
Never assume it is local; see [node resolution](node-resolution.md).

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

# Work temp

`/tmp` stays a 64 MiB noexec tmpfs. Modpack downloads, archive extracts, and
native libraries such as Netty and c2me use `/data/.tmp` on the server data
bind. That directory is on the same disk as the world, it is executable, and
it is created mode 01777 by the panel when the data path is local or by a
short-lived container on the daemon when the node is remote. `TMPDIR` and
`-Djava.io.tmpdir` both point there. Scheduled world backups skip `.tmp`.

# Runtime profile

Before the daemon is asked to create a container, `docker.PreflightRuntime`
describes the request that was actually resolved: the user the container runs
as, whether the root is read-only, the writable paths, the capability set, the
tmpfs mounts and the network mode. A fatal finding refuses the create with
`ErrRuntimeIncompatible` and a named reason, so an unsupported image, mount or
override fails before it replaces a working container. A warning is logged and
does not block. The rules and their remedies live in
`internal/docker/runtime_profile.go`.

The profile also reports the limits that are actually applied, not the ones
that were requested: memory and memory+swap, the process limit, the CPU cap,
and the writable-layer cap. A limit the daemon will not apply is reported as
zero, and a request that cannot take effect is a named warning. The world is a
bind mount, so it is never quota-limited by the daemon: `storage-opt size`
sizes only the container writable layer, is applied only when the root is
writable, and is reported as `storage_quota_is_not_a_world_quota` whenever it
is set. World growth is measured by the metrics collector against the node
filesystem instead.

# How to check

```sh
go build ./...
go vet ./...
go test ./...
```

# Read next

- [RPC contract](rpc-contract.md)
- [Persistence](persistence.md)
- [Scheduler](scheduler.md)
