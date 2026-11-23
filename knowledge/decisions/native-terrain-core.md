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
read accessor. The core takes an authorized caller's canonical level directory
and serves the vanilla Overworld, Nether and End dimension spans. It exposes no
route and supplies no authorization. The caller owns the finite worker pool,
cache, and delivery policy.

# Alternatives

A game-side plugin or external renderer would require an installation on each
server. A textured block renderer would also need broader format coverage and
a game asset source. Reading surface blocks directly from Anvil lets the map
distinguish common materials using an original palette.

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
after wiki-verified format research, game-save fixture qualification still
runs per release) and a per-dimension span check on yPos, heightmap packing
and section count, so unknown formats fail closed instead of misrendering.
LZ4 region chunks (compression byte 4, the lz4-java block stream) decode
within the same budgets; `.mcc` external chunks stay unsupported. Tiles carry
an opaque region-file freshness validator (size + mtime); the transport
answers conditional revalidation with 304 before spending render budget, and
the client's live toggle paces rechecks without changing the shared worker
budget. The Nether's bedrock roof renders transparent so terrain below it
stays visible. Bukkit sibling worlds and modded dimension folders are out of
scope.
