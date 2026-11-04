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

`/health/detail` reports five layers and never rounds one into another:

| Layer | Meaning | Fails the endpoint |
| --- | --- | --- |
| `process` | the API is serving | no |
| `database` | the database ping succeeded | yes, `503` |
| `runtime` | the Docker daemon answered within 2s | no |
| `game` | stored server statuses and their age | no |
| `route` | the proxy is running when it is enabled | no |

A server's stored status is evidence only while it is fresh (two minutes). An
older status counts as `stale`, and a game layer where every status is stale is
`unknown`, not `ok`. An unobserved layer is `unknown` as well: a missing check
is never reported as healthy.

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
go test ./internal/health ./internal/rpc/handlers
```

# Read next

- [Runtime model](../architecture/runtime.md)
- [Lifecycle journal](lifecycle-journal.md)
