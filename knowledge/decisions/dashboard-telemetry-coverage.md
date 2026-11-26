---
type: Decision
title: Dashboard telemetry coverage
description: Dashboard player and RAM totals describe running servers that reported telemetry.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/routes/+page.svelte
  - web/carbon-panel/src/lib/features/servers/dashboard-telemetry.ts
  - scripts/manual-review/e2e/dashboard-telemetry.mjs
  - internal/metrics/collector.go
  - internal/rpc/services/server_join_observation.go
sources:
  - title: Dashboard telemetry
    resource: https://carbonpanel.atlassian.net/browse/OSS-438
  - title: Frontend server inventory ownership
    resource: server-inventory.md
  - title: Reported Server fields
    resource: ../../proto/carbonpanel/v1/common.proto
---

# Choice

The dashboard reports observations for running servers. A current playable
join observation can report zero players. Server RPCs emit RAM only when the
last successful Docker stats poll started within two configured poll intervals
for the current container and after its latest recorded start. RCON, disk, and
SLP refreshes cannot extend that window. The legacy scalar uses zero when no
current RAM sample is available; only positive values report usage. Partial
totals identify their reporting coverage.

RAM use and its utilization compare the same reporting subset. A reporting
server with no configured limit makes the percentage unavailable. Fleet RAM
allocation and configured player capacity remain independent of reported usage.

# Alternatives

- The aggregate cache timestamp can be refreshed by disk or player data and
  would present an old Docker value as current.
- Polling Docker stats on each inventory request would couple page latency to
  every daemon instead of reusing bounded collector observations.
- A new presence field could distinguish true zero RAM from missing usage.
  Resource-specific expiry solves the stale-positive case without a wire change.

# Consequence

The packaged dashboard fixture drives the actual route through the Connect
response boundary for unavailable, measured-zero-player, and partial cases.
