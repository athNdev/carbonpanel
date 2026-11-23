---
type: Architecture
title: Player management
description: Player identity, provenance, moderation and capability gates for the PlayerService surface.
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/player.proto, internal/rpc/services/player.go, internal/rpc/services/player_wiring.go, internal/db/player.go, internal/playeradapters, internal/playerskin, internal/playerdata, internal/rbac/mapping.go]
sources:
  - title: Player identity, provenance and capability gates
    resource: ../../docs/adr/0017-player-management-identity-and-capability-gates.md
  - title: Add an RPC
    resource: ../workflows/add-rpc.md
---

# Parts

- [Player management - Identity, provenance and capabilities](player-management-identity-and-provenance.md) — PlayerIdentity is a panel cache/index with an opaque pl_* key. Online UUID,
- [Player management - Snapshot resolution](player-management-snapshot-resolution.md) — Player snapshots live in the server's active level folder under the dashed UUID file name.
- [Player management — Authorization and audit](player-management-authorization-and-audit.md) — All PlayerService procedures are mapped to players:* permissions and
