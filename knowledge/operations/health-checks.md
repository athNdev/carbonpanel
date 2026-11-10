---
type: Runbook
title: Health and readiness
description: Which endpoint answers which question, and why a degraded node must not stop the API.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/health
  - internal/rpc/server.go
  - internal/rpc/handlers/health.go
  - internal/docker/pool.go
  - internal/metrics/collector.go
  - internal/rpc/services/support.go
sources:
  - title: Container privileges invariant
    resource: ../invariants/container-privileges.md
---

# Use this when

Wiring a deployment probe, debugging a report that a server is "down", or
deciding whether a green endpoint proves a player can join.

# Endpoints

- `GET /healthz` — the process is alive. No dependency is checked.
- `GET /readyz` — the process is alive and the database answers. A failure is
  `503`.
- `GET /health/detail` — every layer with its own state and reason.

# Layers

`/health/detail` reports five layers and never rounds one into another. Each
component carries `source`, `observedAt` and a reason. A timeout or recovery
is that reason; unknown is never rewritten as stopped or missing.

| Layer | Meaning | Fails the endpoint |
| --- | --- | --- |
| `process` | the API is serving | no |
| `database` | the database ping succeeded | yes, `503` |
| `runtime` | each enabled Docker node answered within 2s | no |
| `game` | stored status plus fresh SLP or RCON join evidence | no |
| `route` | the proxy is running when it is enabled | no |

The runtime layer fans out `ClientPool.ProbeNodes`. One unreachable node is
`degraded` (or `unavailable` when every enabled node fails). The JSON stays an
unauthenticated aggregate: counts and reasons, no hostnames or secrets. When
the node inventory cannot be listed the layer is `unknown`, not down. When no
enabled nodes are registered the default Docker client is pinged instead.

A server's stored status is evidence only while it is fresh (two minutes). An
older status counts as `stale`, and a game layer where every status is stale is
`unknown`, not `ok`. `StatusRunning` is not playable by itself. Playable
requires a fresh successful SLP observation, or a fresh RCON success when SLP
was never collected. A running server with no join probe is `unobserved`
(unknown), not stopped. A fresh failed SLP is `unjoinable`. Starting or
creating past the three-minute startup deadline is `timedOut`
(`degraded`/`unavailable` with a reason), never silently playable or stopped.
An unobserved layer is `unknown` as well: a missing check is never reported as
healthy. `health.ClassifyServer` is the per-row form of that game layer: a
stale status, a missing join probe and a failed join stay distinct, and a
last-good join timestamp is kept when the current reading is stale. The
support bundle carries `observations.json` with that projection so an operator
can collect last-good evidence without a Docker socket. Player samples, MOTD
and hostnames are omitted.

# Why a runtime outage is not a `503`

`/readyz` gates traffic to the API. A remote Docker node that is down does not
stop the API from serving the console, the catalogue or another node's servers.
Failing the probe would make the deployment restart a healthy process and take
the remaining nodes down with it. The layered endpoint reports the node as
unavailable while the process stays ready. See
[container privileges](../invariants/container-privileges.md) for the
per-container view.

# How to check

```sh
go test ./internal/health ./internal/rpc ./internal/rpc/handlers ./internal/docker ./internal/metrics ./internal/reconciler
```

# Read next

- [Runtime model](../architecture/runtime.md)
- [Lifecycle journal](lifecycle-journal.md)
