---
type: Decision
title: Frontend server inventory ownership
description: Server reads, lifecycle requests and status presentation share generation guards and account-scoped state.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/features/servers
  - web/carbon-panel/src/lib/stores/servers.ts
  - web/carbon-panel/src/routes/+layout.svelte
  - web/carbon-panel/src/routes/+page.svelte
  - web/carbon-panel/src/routes/servers
  - scripts/manual-review/e2e/server-inventory-check.mjs
sources:
  - title: Inventory consolidation
    resource: https://carbonpanel.atlassian.net/browse/OSS-339
  - title: Server response construction
    resource: ../../internal/rpc/services/server.go
---

# Choice

One inventory owns hydration, list errors, in-flight read deduplication and
refresh subscriptions. Request sequences protect newer server observations;
local mutations invalidate older reads, and removed IDs remain tombstoned until
an account reset. Account generations also isolate pending action outcomes.
Matching refresh subscriptions share a timer and release it on the last close.
Login/logout clears inventory and invalidates pending reads before another
account can populate it. Views retain confirmation, copy and navigation.

ListServers and GetServer currently both apply cached metrics and use the same
complete Server conversion. A newer complete response is authoritative even
when its fields are zero, false or empty: preserving truthy values would revive
stale join/player evidence. Older list/detail completions cannot replace newer
observations. A future sparse response requires an explicit field contract.

Shared actions construct generated RPC payloads and own in-flight idempotency.
Delete, keep-data and detach retain different flags; export only creates a
world download and never deletes inventory. Recreate has no backend request ID
field, so the frontend deduplicates its in-flight call. Notifications and the
actual download stay in the view. ERROR is red and UNHEALTHY is yellow in the
shared presentation policy.
