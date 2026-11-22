---
type: Architecture
title: Player identity and provenance
description: How identities are registered versus observed, and where skins resolve.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/db/player.go
  - internal/rpc/services/player.go
  - internal/playerskin
  - web/carbon-panel/src/lib/features/players
sources:
  - title: ADR 0054
    resource: ../../docs/adr/0054-player-last-seen-semantics.md
---

# Identity, presence and skins

- `last_seen_at` is an observation written only by ingestion
  (sessions/roster/metrics). Operator paths register via
  `EnsurePlayerIdentityRecorded` / `EnsureOfflinePlayerIdentity` and never
  stamp it. ADR 0054 is the contract.
- Presence derives from observations: no last-seen and no session rows means
  "Never observed" — the UI must not render a time, and never-observed rows
  sort after observed ones in both directions.
- Actions need a server: a roster row without a session falls back to its
  allowlist/access-state server; an empty server id is `InvalidArgument`, not
  "server not found".
- Skins resolve name→UUID through the server's own `usercache.json`
  (`internal/playerskin/usercache.go`, v4 UUIDs only, 15-minute cache,
  fail-soft) before any Mojang query. A panel-derived offline pseudo-UUID
  never queries Mojang; full-skin rendering uses the same-origin
  `GetPlayerSkin kind:'skin'` pipeline.

# Read next

- [ADR 0054](../../docs/adr/0054-player-last-seen-semantics.md)
- [Provider adapters](../decisions/provider-adapters.md)
