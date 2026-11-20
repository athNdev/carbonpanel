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
sources:
  - title: Dashboard telemetry
    resource: https://carbonpanel.atlassian.net/browse/OSS-438
  - title: Frontend server inventory ownership
    resource: server-inventory.md
  - title: Reported Server fields
    resource: ../../proto/carbonpanel/v1/common.proto
---

The dashboard reports observations for running servers. A current playable
join observation can report zero players. The legacy RAM scalar has no
presence or freshness marker, and only a positive value is distinguishable
from missing telemetry. With no report, the metric is unavailable. Partial
totals identify their reporting coverage.

RAM use and its utilization compare the same reporting subset. A reporting
server with no configured limit makes the percentage unavailable. Fleet RAM
allocation and configured player capacity remain independent of reported usage.

The packaged dashboard fixture drives the actual route through the Connect
response boundary for unavailable, measured-zero-player, and partial cases.
