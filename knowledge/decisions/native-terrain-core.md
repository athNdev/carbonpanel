---
type: Decision
title: Native terrain reads saved Java surfaces
description: A module-free reader renders bounded Java Overworld tiles from saved heightmaps and block palettes.
status: stable
owners: [oss-runtime]
code_refs: [internal/worldterrain, internal/worldlib/chunk_surface.go, internal/worldlib/nbt.go]
sources:
  - title: Terrain formats, limits and verification
    resource: ../../docs/world-terrain.md
  - title: World View behavior
    resource: ../architecture/world-viewing.md
  - title: Native Java renderer
    resource: https://carbonpanel.atlassian.net/browse/OSS-442
---

# Choice

Render observations from standard-height Java Overworld Anvil files with the
owning server's confined read accessor. The core takes an authorized caller's
canonical level directory. It exposes no route and supplies no authorization.
The caller owns the finite worker pool, cache, and delivery policy.

The Vanilla End reader uses a fixed DIM1/region profile with standard End
heights and Java 1.21.9 chunks. It shares the confined reader and publishes
no browser capability. OSS-440 tracks qualification of the packaged map.

# Alternatives

A game-side plugin or external renderer would require an installation on each
server. A textured block renderer would also need broader format coverage and
a game asset source. Reading surface blocks directly from Anvil lets the map
distinguish common materials using an original palette. A qualified
controller-local Vanilla Java 1.21.9 save can also render its saved
`DIM1/region` End through the same bounded reader; custom End generators stay
outside the profile.

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
