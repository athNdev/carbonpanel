---
type: Architecture
title: "RPC contract — Rule (part 2)"
description: "Packwiz pack control and staged config rollout are further compatibility"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1, internal/rpc, buf.gen.yaml, web/carbon-panel/src/lib/proto, internal/nodescan]
sources:
  - title: Parent concept
    resource: rpc-contract.md
---
Packwiz pack control and staged config rollout are further compatibility
migrations. Generated `PackwizService` (ListPacks, GetPack, ClonePack,
RefreshPack, CheckUpdates, DeletePack, AddPackMod, AddUrlMod, UpdatePackMod,
DeletePackMod, BatchPackMods) and the legacy `/api/v1/packwiz/packs` routes
share one `handlers.PackwizOperations`; modpacks/read for read methods and
modpacks/create for pack and mod mutations use the legacy global `*` scope.
The mod-add operation keeps the soft dependency-resolution result so a resolver
failure does not undo the already-installed requested mod. Generated
`StagedConfigService` (ListStagedChanges, StageConfigChange, ApplyStagedChange,
RollbackStagedChange, DiscardStagedChange) and the legacy
`/api/v1/staged-config/{server}/staged` routes share one
`handlers.StagedConfigOperations`; server_config/read for list and
server_config/create for mutations are scoped to the request's `server_id`, and
the generated service re-authorizes the staged record's real owner before
mutation so a path server cannot mask another owner. Both families fail closed
with a nil enforcer. Multipart import, upload-session bytes, raw pack files and
export archives stay on HTTP; create/update/import and migrate/deploy pack
operations remain adapter-only until a later slice. Pack files, datapacks,
content edits and the unauthenticated loader-version catalog also remain
adapter-only: they need their own boundary or scope decision rather than a
mechanical client switch. Legacy routes remain for the ADR 0012 window.

Online mod browse is the second compatibility migration: generated
`ModBrowseService` (SearchMods, GetModVersions, InstallMods) and the legacy
`/api/v1/servers/{id}/mods/*` JSON routes share one `ModOnlineManager` operation.
Browse/version lookup require mods/read and install requires mods/create, both
on the global `*` object; a server-scoped grant cannot authorize global browse
and Studio's `none` browse mode stays authorized. The console dialog uses the
generated client; Studio callers still use the adapter during the deprecation
window.

Online mod installs stream to confined node-local staging. Completion publishes
the whole JAR into the mapped mods directory, so an interrupted retry leaves the
previous file usable.
ModBrowse preserves required and incompatible declarations from its providers.
Create Server resolves exact requirements across the selected mod set and
rejects provider-declared incompatibilities before applying it.
