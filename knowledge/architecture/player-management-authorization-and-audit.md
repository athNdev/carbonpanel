---
type: Architecture
title: "Player management — Authorization and audit"
description: "All PlayerService procedures are mapped to players:* permissions and"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/player.proto, internal/rpc/services/player.go, internal/rpc/services/player_wiring.go, internal/db/player.go, internal/playeradapters, internal/playerskin, internal/playerdata, internal/rbac/mapping.go]
sources:
  - title: Parent concept
    resource: player-management.md
---
# Authorization and audit

All `PlayerService` procedures are mapped to `players:*` permissions and
server-scoped procedures use `server_id` so subuser grants apply. Moderation
uses a request id, a journal row and an `ActivityLog` entry with subject type
`player` and an independent server ID. The server audit query includes only
explicitly scoped player events. Historical player events with no server ID
remain available through the global audit API; identity and request IDs do
not establish the server. Undo exists only where a prior value was recorded; kick is explicitly
not undoable. The legacy `SendCommand` string path remains `servers:command`
and is not a moderation boundary.
