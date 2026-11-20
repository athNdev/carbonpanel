---
type: Decision
title: Modpack display loader
description: A modpack server keeps its provisioning platform in ModLoader and names the real game loader in ModpackLoader for display.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/rpc/services/modpack_loader.go
  - internal/rpc/services/server.go
  - proto/carbonpanel/v1/common.proto
  - web/carbon-panel/src/lib/utils/mod-loader.ts
  - web/carbon-panel/src/routes/servers/new/+page.svelte
sources:
  - title: Repository map
    resource: ../repo-map.md
---

# Decision

- `Server.ModLoader` keeps the provisioning platform (`modrinth`,
  `auto_curseforge`) because the container `TYPE` must stay the platform so
  the image fetches the pack. Never "fix" a modpack display by rewriting it.
- `Server.ModpackLoader` carries the real game loader (fabric, forge,
  neoforge, ...) resolved from the indexed modpack at attach time, and the
  console displays and filters mod indexes by it whenever the stored loader
  is a platform. Empty means no modpack or an unknown loader.
- The create wizard sends the provider's unique release ID. On attach, an
  exact indexed file supplies the loader. If a pinned Modrinth release is
  absent from the file index, the panel briefly consults the provider version
  list. A unique legacy version number can resolve to an exact release ID.
  Provider errors leave the requested runtime selector and pack-level
  display fallback in place.
- Older platform-loader rows converge on read: listing or opening the server
  resolves the loader from its stored pack config once and persists it.
  Resolution failures keep showing the platform, exactly as before.

# Rationale

Showing the platform as the mod loader is inaccurate and it silently
unfiltered the mod browser. Splitting provisioning from display fixes both
without changing what boots.

# Verification

`modpack_loader_test.go` covers pinned creation with and without indexed
files, provider unavailability, platform skipping and the empty-to-UNSPECIFIED
mapping. `mod-loader.test.ts` covers the effective
loader used by the console.
