---
type: Architecture
title: "Runtime model — Shape + Listeners + Build boundary"
description: "cmd/carbon-panel/main.go loads configuration, opens the SQLite database,"
status: stable
owners: [oss-runtime]
code_refs: [cmd/carbon-panel, internal/rpc, internal/proxy, internal/db, internal/docker, internal/docker/resource_enforcement.go, pkg/runtimeprofile, web/carbon-panel/src/lib/utils/resource-enforcement.ts, pkg/files, internal/scheduler, internal/operation, internal/reconciler, web/carbon-panel]
sources:
  - title: Parent concept
    resource: runtime.md
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
the proxy route, or log subscriptions stuck. Failing healthchecks while a
server is still starting keep the boot status; only a running server adopts
unhealthy, which the console renders as degraded yellow rather than error red.
A game process that dies inside a live container is detected from the current
boot's log tail (hard-dependency failures, the image runner's exit report)
and recorded as error with a human-readable reason, which the console shows
on the status tile.

# Listeners

- `:8080` — Connect-RPC, HTTP and the WebSocket hub.
- `:25565` — the public TCP proxy. See [proxy](proxy.md).

# Build boundary

The backend tests expect `web/carbon-panel/build` to exist; CI creates a
placeholder when the frontend is not built. Production builds run
`bash scripts/web-build.sh` first.
