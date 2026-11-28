---
type: Decision
title: Native terrain reads saved Java surfaces
description: A module-free reader renders bounded Java Overworld, Nether and End tiles from saved heightmaps and block palettes.
status: stable
owners: [oss-runtime]
code_refs: [internal/worldterrain, internal/worldlib/chunk_surface.go, internal/worldlib/nbt.go, internal/worldview/native_map.go]
sources:
  - title: Terrain formats, limits and verification
    resource: ../../docs/world-terrain.md
  - title: World View behavior
    resource: ../architecture/world-viewing.md
  - title: Native Java renderer
    resource: https://carbonpanel.atlassian.net/browse/OSS-442
  - title: Live terrain across modloaders and versions
    resource: https://carbonpanel.atlassian.net/browse/OSS-463
---

# Choice

Render observations from Java Anvil files with the owning server's confined
read accessor. The core takes an authorized caller's canonical level directory,
serves the vanilla Overworld, Nether and End dimension spans without exposing
a route or authorization, and leaves the finite worker pool, cache and delivery
policy to the caller.

# Alternatives

A game-side plugin or external renderer would require an installation on each
server. A textured block renderer would also need broader format coverage and
a game asset source. Direct Anvil block reads distinguish common map materials.

# Consequences

The core reads one region per tile and bounds chunk count, compressed bytes,
decoded bytes and values, coordinates, and output dimensions. A changed selected region
entry invalidates the result. Unsupported and unreadable data remain visible
in coverage counts. Region timestamp values describe saved chunks, while the
PNG uses material colors, water depth and relief. Columns without recognized
surface blocks use elevation colors. The overview preview frames saved surface
pixels around spawn and leaves undrawn gaps transparent.
Supported late generation stages can contribute provisional saved surface
heights. The PNG fades these chunks and returns their count. The preview
displays their observed colors with full opacity. Early generation
records without qualified surfaces remain transparent. Viewing never generates terrain.
Accepted chunk layouts are pinned two ways: a DataVersion ceiling (raised only
after wiki-verified format research, with game-save qualification per release)
and per-dimension checks of yPos, heightmap packing and logical section count,
so unknown formats fail closed. The Nether separates its 256-block storage
height (nine-bit, 37-long heightmaps) from its 128-block logical height (eight
sections and the bedrock roof at y=127); the Overworld and End keep 24 and 16
full-height sections. LZ4 region chunks (compression byte 4, the lz4-java block
stream) decode within the same budgets; `.mcc` external chunks stay unsupported. Tiles
carry an opaque region-file freshness validator (size + mtime); the transport
re-checks the scoped world before answering conditional revalidation with 304,
so a world change is refused before render budget. The client's live toggle
paces rechecks without changing the shared worker budget or the tile cache. The Nether's WORLD_SURFACE is its full
bedrock roof, so the surface scan descends to the first terrain block under
the roof; columns that hold only roof bedrock and air stay transparent.
Bukkit sibling worlds and modded dimension folders are out of scope.
