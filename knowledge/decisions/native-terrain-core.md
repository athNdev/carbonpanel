---
type: Decision
title: Native terrain reads saved Java elevations
description: A module-free reader renders bounded Java Overworld tiles from saved heightmaps.
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

# Alternatives

A game-side plugin or external renderer would require an installation on each
server. A textured block renderer would also need broader format coverage and
a game asset source. Reading saved heightmaps keeps the first renderer usable
without either prerequisite.

# Consequences

The core reads one region per tile and bounds chunk count, compressed bytes,
decoded bytes and values, coordinates, and output dimensions. A changed selected region
entry invalidates the result. Unsupported and unreadable data remain visible
in coverage counts. Region timestamp values describe saved chunks, while the
PNG represents elevation and slope without claiming block or biome fidelity.
