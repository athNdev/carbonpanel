---
type: Runbook
title: "Health and readiness — Use this when + Endpoints"
description: "Wiring a deployment probe, debugging a report that a server is \"down\", or"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: health-checks.md
---
# Use this when

Wiring a deployment probe, debugging a report that a server is "down", or
deciding whether a green endpoint proves a player can join.

# Endpoints

- `GET /healthz` — the process is alive. No dependency is checked.
- `GET /readyz` — the process is alive and the database answers. A failure is
  `503`.
- `GET /health/detail` — every layer with its own state and reason.
