---
type: Decision
title: Modpack content kinds
description: Datapacks, resource packs and shader packs share one packwiz contract in Modpack Studio; a server bake installs only server-side content.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/packwiz/content.go
  - internal/minecraft/content.go
  - internal/rpc/handlers/packwiz_content.go
  - web/carbon-panel/src/lib/components/pack-content-panel.svelte
sources:
  - title: Packwiz pack format
    resource: https://packwiz.infra.link/tutorials/creating/pack/
---

# Decision

- All three kinds use one generic route set:
  `POST /api/v1/packwiz/packs/{id}/content` and
  `PATCH|DELETE .../content/{kind}/{slug}`, with `search`, `versions`, `url`
  and `upload` helpers. A new kind extends the kind enum, not the route
  surface.
- Metafiles live in packwiz's category folders (`mods/`, `datapacks/`,
  `resourcepacks/`, `shaderpacks/`). `[options] datapack-folder` records where
  datapacks install and is written only when the pack has datapacks.
- Every configurable packwiz option round-trips: `side`, top-level `pin`, and
  `[option] optional`, `default` and `description`. An empty `side` is omitted
  rather than written.
- Uploads and URL-added archives are validated before a metafile exists:
  datapack needs a root `pack.mcmeta`; resource pack needs a root `pack.mcmeta`
  plus `assets/`; shader pack needs `shaders/`. A zip wrapped in a single
  top-level directory is rejected rather than guessed at.
- A server bake writes mods to the mods directory and datapacks to
  `<data>/<level-name>/datapacks` (default `world`). Resource packs and shader
  packs are client-side: bake never writes them and reports them as
  client-only so the UI points at sync or export instead.
- CurseForge export refuses with the complete item list when anything would be
  dropped, rather than shipping a partial pack.

# Rationale

One contract keeps the studio panel, handlers and metafile writer aligned and
stops three near-identical implementations from drifting. Validating before
writing the metafile stops a broken archive from becoming a pack entry.

# Verification

`internal/packwiz/content_test.go` covers option round-trips, folder moves,
upload validation, every-kind mrpack export and bake selection;
`internal/rpc/handlers/packwiz_content_test.go` covers the RPC routes;
`pack-content-panel.test.ts` covers the console contract.
