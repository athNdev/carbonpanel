---
type: Architecture
title: "Player management - Snapshot resolution"
description: "Player snapshots live in the server's active level folder under the dashed UUID file name."
status: stable
owners: [oss-runtime]
code_refs: [internal/playerdata, internal/rpc/services/player.go, internal/db/server_level.go]
sources:
  - title: Player management
    resource: player-management.md
  - title: "ADR 0017: Player management identity and capability gates"
    resource: ../../docs/adr/0017-player-management-identity-and-capability-gates.md
---
# Snapshot resolution

Minecraft writes the snapshot files into the server's active level folder:
`<data-root>/<ActualLevelName()>/playerdata/<uuid>.dat` for character snapshots,
`<data-root>/<ActualLevelName()>/stats/<uuid>.json` for stats, and
`<data-root>/<ActualLevelName()>/advancements/<uuid>.json` for advancements. The
reader tries that location first and keeps the root-relative location as a
fallback, so a server whose data root is the world folder still resolves. The
level name comes from the server's own configuration; an unknown configuration
falls back to the vanilla `world` folder.

The dashed UUID file name takes precedence across both the active-level and
root-relative locations, even when an undashed file exists in the active level.
The undashed file name is accepted only as a fallback for tooling that copies
the file under that name.

`GetPlayerDeepData` requires a server scope: without a server there is no
snapshot to read, so the request is refused instead of looking for a server
that was never named. The player detail page skips the request and says so
when the identity is not linked to a server.

The skin asset is fetched through the same-origin RPC and rendered from an
object URL the page created, so the panel CSP has to allow `blob:` in
`img-src`; without it the browser refuses the image and every avatar renders
broken.
