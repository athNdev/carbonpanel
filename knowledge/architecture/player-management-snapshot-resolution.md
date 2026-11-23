---
type: Architecture
title: "Player management - Snapshot resolution"
description: "Player snapshots live in the server's active level folder under the dashed UUID file name."
status: stable
owners: [oss-runtime]
code_refs: [internal/playerdata, internal/rpc/services/player.go, internal/db/server_level.go]
sources:
  - title: Parent concept
    resource: player-management.md
---
# Snapshot resolution

Minecraft writes the snapshot files into the server's active level folder:
`<data-root>/<ActualLevelName()>/<kind>/<uuid>.<ext>`. The reader tries that
location first and keeps the root-relative location as a fallback, so a server
whose data root is the world folder still resolves. The level name comes from
the server's own configuration; an unknown configuration falls back to the
vanilla `world` folder.

The dashed UUID file name is tried first and the undashed one is accepted as a
fallback for tooling that copies the file under that name.

`GetPlayerDeepData` requires a server scope: without a server there is no
snapshot to read, so the request is refused instead of looking for a server
that was never named. The player detail page skips the request and says so
when the identity is not linked to a server.

The skin asset is fetched through the same-origin RPC and rendered from an
object URL the page created, so the panel CSP has to allow `blob:` in
`img-src`; without it the browser refuses the image and every avatar renders
broken.
