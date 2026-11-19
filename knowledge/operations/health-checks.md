---
type: Runbook
title: Health and readiness
description: Which endpoint answers which question. The support bundle records stale last-good observations in observations.json; a degraded node must not stop the API.
status: stable
owners: [oss-runtime]
code_refs: [internal/health, internal/rpc/server.go, internal/rpc/handlers/health.go, internal/docker/pool.go, internal/metrics/collector.go, internal/rpc/services/support.go, internal/rpc/services/server.go]
sources:
  - title: Container privileges invariant
    resource: ../invariants/container-privileges.md
---

# Parts

- [Health and readiness — Use this when + Endpoints](health-checks-use-this-when.md) — Wiring a deployment probe, debugging a report that a server is "down", or
- [Health and readiness — Layers](health-checks-layers.md) — /health/detail reports five layers and never rounds one into another. Each
- [Health and readiness — Why a runtime outage is not a `503` + How to check + Read next](health-checks-why-a-runtime-outage-is-not-a-503.md) — /readyz gates traffic to the API. A remote Docker node that is down does not

# Key facts

- `/readyz` gates traffic to the API. A remote Docker node that is down does not
- the node inventory cannot be listed the layer is `unknown`, not down. When no
- last-good join timestamp is kept when the current reading is stale. The
- support bundle carries `observations.json` with that projection so an operator
- can collect last-good evidence without a Docker socket. Player samples, MOTD
- the remaining nodes down with it. The layered endpoint reports the node as
- | Layer | Meaning | Fails the endpoint |
- | `runtime` | each enabled Docker node answered within 2s | no |
- `degraded` (or `unavailable` when every enabled node fails). The JSON stays an
- classification for live SLP fields: last-good join is never copied into
- stale node, partial restore/migration and disk exhaustion are in
- go test ./internal/health ./internal/rpc ./internal/rpc/handlers ./internal/docker ./internal/metrics ./internal/reconciler
- Wiring a deployment probe, debugging a report that a server is "down", or
- deciding whether a green endpoint proves a player can join.
- `GET /readyz` — the process is alive and the database answers. A failure is
- `GET /health/detail` — every layer with its own state and reason.
- `/health/detail` reports five layers and never rounds one into another. Each
- | `database` | the database ping succeeded | yes, `503` |
- The runtime layer fans out `ClientPool.ProbeNodes`. One unreachable node is
- enabled nodes are registered the default Docker client is pinged instead.
- older status counts as `stale`, and a game layer where every status is stale is
- `unknown`, not `ok`. `StatusRunning` is not playable by itself. Playable
- (unknown), not stopped. A fresh failed SLP is `unjoinable`. Starting or
- healthy. `health.ClassifyServer` is the per-row form of that game layer: a
- stale status, a missing join probe and a failed join stay distinct, and a
- `join_observation_kind` so stale and unobserved stay distinct from running
- # Why a runtime outage is not a `503`
- stop the API from serving the console, the catalogue or another node's servers.
