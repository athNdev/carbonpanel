---
type: Decision
title: Shared provider request adapters
description: Modrinth and CurseForge request construction is shared while browse and Packwiz retain their presentation and version-selection policies.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/indexers/modrinth
  - internal/indexers/fuego
  - internal/packwiz/dependencies.go
  - internal/packwiz/content.go
  - internal/packwiz/packwiz.go
  - internal/rpc/handlers/mod_search.go
  - internal/rpc/handlers/mod_dep_titles.go
  - internal/rpc/handlers/packwiz.go
  - internal/rpc/services/mod_update_preview.go
  - proto/carbonpanel/v1/mod.proto
  - web/carbon-panel/src/lib/components/server-mods.svelte
sources:
  - title: Provider consolidation
    resource: https://carbonpanel.atlassian.net/browse/OSS-338
---

# Choice

The concrete provider adapters own search, version and project request construction, caller
credentials, instance-owned transport and structured provider failures. Browse
retains its sort/filter choices and HTTP response mapping. Packwiz update checks
retain provider ordering, game/loader compatibility, primary-file choice and
SHA-512 preference over SHA-1. An operation context reaches update-check requests.

Installed-file previews identify active and disabled files by SHA-512. Modrinth filters listed releases by game, loader and server support.
Unknown files, ambiguous artifacts and unresolved dependencies remain visible. Checking writes no server state; provider lookup failures are unavailable, never unidentified uploads, and their reason is the fixed "Provider lookup failed: Try again later." text without provider diagnostics. Application and scheduled policies reuse the staged batch through one planner; see [Reviewed mod update application](mod-update-apply.md).

# Consequences

Browse and update checks use the same transport and redirect policy as the
indexer. Compatibility wrappers retain existing callers. Packwiz still reports
an unavailable update as unchanged when the provider fails; cancellation returns
an error rather than a successful empty check. Dependency lookup and compatibility checks use the same adapters. Dependency
resolution keeps primary-file selection; legacy mod updates and migration
previews keep first-file selection. CurseForge dependency lookup retains the
last matching loader entry, required/optional relations and CDN URL synthesis.
Read-only compatibility previews propagate cancellation. The generated
`ModBrowseService` and the legacy mod JSON routes delegate to the same
`ModOnlineManager` operation, so transport mapping cannot fork provider,
credential, side-filter or install-audit policy. Browse retains
Minecraft-version relaxation after an empty search and local version filtering
with an all-files fallback. Dependency titles remain best-effort: at most 200
projects per batch, then individual lookups only for at most 10 IDs. Cancellation
stops fallback work. Datapack search retains class, sort, ZIP selection and hash
policies while using the same provider request adapters.

Browse adopts the CurseForge adapter failure policy: malformed official JSON,
network and HTTP errors can fall back once to the keyless provider; if both
fail, the primary error is retained. Slug resolution keeps its community-first
order and only retries the official endpoint for network or HTTP failures. No
automatic same-endpoint rate-limit retries are added. Existing empty/unchanged
results and raw dependency IDs on failure remain a presentation choice; explicit
degraded-result contracts need separate API/UI design.
