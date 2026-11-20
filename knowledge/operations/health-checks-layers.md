---
type: Runbook
title: "Health and readiness — Layers"
description: "/health/detail reports five layers and never rounds one into another. Each"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: health-checks.md
---
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
stale status, a missing join probe and a failed join stay distinct, and
last-good join and status observation timestamps are kept when the current
reading is stale. The support bundle carries `observations.json` with that
projection so an operator can collect last-good evidence without a Docker
socket. Server and node readings in that bundle include safe, actionable
recovery guidance without paths, hostnames, credentials or other secrets.
Player samples, MOTD and hostnames are omitted. `GetServer` and `ListServers`
use the same classification for live SLP fields: last-good join is never copied
into `slp_available`, `players_online` or `player_sample`. The proto carries
`join_observation_kind` so stale and unobserved stay distinct from running
success and from a failed join. Recovery paths for permission, port, pull,
stale node, partial restore/migration and disk exhaustion are in
[actionable recovery](actionable-recovery.md).
