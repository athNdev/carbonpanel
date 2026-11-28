---
type: Decision
title: Plugin indexer seam
description: Hangar and Modrinth plugin discovery behind a PluginIndexer registry, gated by one loader-capability predicate.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/indexers/plugin.go
  - internal/indexers/hangar
  - internal/rpc/handlers/mod_search.go
sources:
  - title: Provider adapters
    resource: provider-adapters.md
---

# Plugin discovery seam

- `internal/indexers/plugin.go` mirrors the datapack registry with a
  `PluginIndexer` interface and factory registry; Hangar implements it as the
  plugin-native provider for Paper-family loaders.
- Loader capability is derived from
  `minecraft.GetModLoaderInfo(loader).ModsDirectory == "plugins"` — the same
  predicate the install path and proto `supports_plugins` use — so
  classification, search routing and write target cannot disagree.
- Search merges Hangar with Modrinth `project_type:plugin` results; every
  result keeps its own source label. Hangar slugs are globally unique and
  resolve through the bare project page.
- Version lookup routes an explicit recognized provider directly to that
  provider. Automatic Hangar-to-Modrinth fallback runs only when the request
  omitted the provider, so a slug collision cannot replace the selected
  provider's artifact.
- Hangar has no server-side game-version filter: compatibility filters
  client-side from `platformDependencies`, segment-aware (1.21 matches 1.21.1,
  not 1.21.10). A listed `LATEST` or an inclusive numeric range such as
  `1.8-1.21.10` covers the versions it names.
- Plugin jars are classified by `plugin.yml` / `paper-plugin.yml`
  (`minecraft.InspectModSide` → `SidePlugin`), checked before fabric/quilt
  descriptors.

# Read next

- [Provider adapters](provider-adapters.md)
