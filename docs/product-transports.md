# Product transport inventory

The console's administrative and product JSON contract is generated from
`proto/carbonpanel/v1`. [ADR 0012](adr/0012-product-transports.md) defines the
compatibility window and migration sequence. Existing HTTP consumers are live
contracts; routes are retained until equivalent behavior, permission scope,
error mapping and generated clients are verified.

| HTTP family | Current first-party consumers | Shared behavior and migration boundary |
| --- | --- | --- |
| `/api/v1/nodes/scan` | Compatibility integrations; `node-settings.svelte` uses generated NodeService.ScanNodes | Shared bounded daemon discovery plus known-node count; nodes/read on global scope. Adapter retains numeric legacy candidate fields. |
| `/api/v1/settings/validate-key` | Compatibility integrations only; `api-keys-settings.svelte` uses generated ConfigService.ValidateProviderKey | Provider credential validation; settings/read on global scope. Both transports share indexers.ValidateProviderKey and the requireGlobalObject policy. Provider validation results remain distinct from transport/authentication failures. |
| `/api/v1/servers/{id}/mods/{search,install}` and `/{project}/versions` | `mod-browser-dialog.svelte` uses generated `ModBrowseService`; Studio detail and `studio/dependencies.ts` still use the compatibility adapter | Generated `ModBrowseService` (`SearchMods`, `GetModVersions`, `InstallMods`) and the legacy JSON routes share one `ModOnlineManager` operation. `mods/read` for browse/versions and `mods/create` for install on the global `*` object match the legacy adapter and allow the explicit Studio `none` browse mode. Dependency selection, filename confinement, per-item errors and install audit stay in the shared operation. Studio callers migrate in a later slice; the adapter remains live for the ADR 0012 window. |
| `/api/v1/packwiz/packs` and `/{id}` | Studio list/detail (JSON adapter retained for the ADR 0012 window) | Generated `PackwizService` covers `ListPacks`, `GetPack`, `ClonePack`, `RefreshPack`, `CheckUpdates` and `DeletePack`; create/update/import remain on the adapter for a later slice. Both transports share one `PackwizOperations` instance. `modpacks/read` for list/get/check-updates and `modpacks/create` for clone/refresh/delete on the global `*` object match the legacy handler. Multipart import, upload-session byte routes, raw pack files and export archives stay HTTP. |
| `/api/v1/packwiz/packs/{id}/{refresh,updates,migrate,mc-version,deploy}` | Studio detail and deploy dialog | Refresh and update-check now also have generated `PackwizService` methods sharing the pack operation; migrate/mc-version/deploy stay on the adapter for a later slice. Recovery boundaries are unchanged. |
| `/api/v1/packwiz/packs/{id}/mods` (add, PATCH, DELETE, batch, direct URL) | Studio mod management and dependency reconciliation | Generated `PackwizService` adds `AddPackMod`, `AddUrlMod`, `UpdatePackMod`, `DeletePackMod` and `BatchPackMods`; the compatibility JSON routes and the generated methods call the same `PackwizOperations` methods. `modpacks/create` on the global `*` object matches the legacy adapter, including the soft dependency-resolution result and best-effort batch semantics. Studio call sites still use the adapter until the OSS-341 extraction sequences the switch. |
| `/api/v1/packwiz/packs/{id}/{datapacks,content,files,datapack-folder}` | Studio detail | Adapter-only in this slice. Content search/version lookup, mutation, upload, datapack install and pack-file editing mix control JSON with multipart bytes and path validation; sharing those operations with a generated contract needs its own boundary review. Raw `pack.toml`, upload sessions and export archives stay HTTP per ADR 0012. |
| `/api/v1/packwiz/loaders/{loader}/versions` | Studio list/detail | Adapter-only. The legacy catalog route is intentionally unauthenticated and sits before the studio management authorization check; giving it a generated RPC would change its resource scope. Any migration requires a separate policy decision, not a mechanical client switch. |
| `/api/v1/staged-config/{server}` and change apply/rollback/delete | Server configuration staged-change controls (JSON adapter retained for the ADR 0012 window) | Generated `StagedConfigService` (`ListStagedChanges`, `StageConfigChange`, `ApplyStagedChange`, `RollbackStagedChange`, `DiscardStagedChange`) and the legacy JSON routes share one `StagedConfigOperations` instance. `server_config/read` for list and `server_config/create` for mutations, scoped to the request's `server_id`; the service re-authorizes the staged record's real owner before any mutation, matching the adapter's dual check. Default apply mode, cron trimming, status filtering and error messages stay in the shared operation. |
| `/api/v2/metrics/` | Metrics integration and administrative metrics clients | Protocol exposition stays HTTP; any JSON administrative controls follow the generated contract policy. |

The handler routers in `internal/rpc/handlers/{node_scan,key_validator,mod_search,
packwiz,staged_config,metrics}.go` remain authoritative for exact method/path
combinations. The table groups operations rather than claiming every suffix is
interchangeable.

## HTTP content and integration surfaces

| Surface | Reason to retain HTTP |
| --- | --- |
| `/api/v1/upload/{session}`, `/api/v1/download/{session}` | Streaming bytes after generated FileService control creates an authorized session. Used by chunked-upload, stream-download and server-files. |
| `/api/v1/packwiz/{id}/{file}` and pack export archives | Packwiz/client content delivery and binary archives, including Studio's exported manifest URLs. |
| `/api/v1/auth/oidc/{login,callback}` | Browser redirects and identity-provider callback protocol. |
| `/api/v1/openapi.yaml` | Generated API documentation consumed by the API viewer. |
| `/healthz`, `/readyz`, `/health/detail` | Operational probes with their existing disclosure and readiness rules. |

## Migration verification

Each converted family needs an application-operation test, a compatibility
JSON/status test, a generated-client positive call and negative permission tests
before its first-party caller switches. Test invalid input, missing resource,
upstream failure and cancellation where the operation supports them. Record
binary/content exceptions in the API viewer; generated control methods appear in
the generated OpenAPI reference. Keeping a compatibility route does not permit a
second implementation of application policy.
