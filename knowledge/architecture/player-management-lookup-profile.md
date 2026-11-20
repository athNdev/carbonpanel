---
type: Architecture
title: Player lookup profile
description: Read-only exact account resolution, bounded skin portraits and authorized session observations.
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/player.proto, internal/playerskin, internal/rpc/services/player.go, web/carbon-panel/src/lib/features/players/PlayerLookup.svelte]
sources:
  - title: Player identity and capability ADR
    resource: ../../docs/adr/0017-player-management-identity-and-capability-gates.md
  - title: Player identity and provenance
    resource: player-management-identity-and-provenance.md
---

# Lookup profile

A caller with global `players:read` can opt into the lookup profile with
`include_skin`. A resolved Mojang UUID can carry a bounded, server-rendered
front portrait from classic, slim, or legacy Java skin atlases. Missing
textures leave the identity result intact. Mojang's legacy HTTP texture
locator supplies only a validated CDN hash; downloads stay on pinned HTTPS.
An exact existing panel identity may link to its record. Lookup can show when
Mojang checked the account. History comes only from a separately authorized
`players:presence` session read. Explicit scoped grants work without server
inventory. A join without visible server metadata shows its time alone.
Lookup never refreshes presence or reads from Minecraft servers.
