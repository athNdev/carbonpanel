# Product transport inventory

The console's administrative and product JSON contract is generated from
`proto/carbonpanel/v1`. [ADR 0012](adr/0012-product-transports.md) defines the
compatibility window and migration sequence. Existing HTTP consumers are live
contracts; routes are retained until equivalent behavior, permission scope,
error mapping and generated clients are verified.

| HTTP family | Current first-party consumers | Shared behavior and migration boundary |
| --- | --- | --- |
| `/api/v1/nodes/scan` | Compatibility integrations; `node-settings.svelte` uses generated NodeService.ScanNodes | Shared bounded daemon discovery plus known-node count; nodes/read on global scope. Adapter retains numeric legacy candidate fields. |
| `/api/v1/settings/validate-key` | `api-keys-settings.svelte` | Provider credential validation; settings/read on global scope. Provider validation results remain distinct from transport/authentication failures. |
| `/api/v1/servers/{id}/mods/{search,install}` and `/{project}/versions` | `mod-browser-dialog.svelte`, Studio detail | Shared provider lookup and install policy; preserve server scope and the explicit Studio `none` browse mode. Dependency selection and install rollback remain application behavior. |
| `/api/v1/packwiz/packs` and `/{id}` | Studio list/detail | List/create/get/update/delete/clone/import pack control; preserve global pack authorization and filesystem ownership. |
| `/api/v1/packwiz/packs/{id}/{refresh,updates,migrate,mc-version,deploy}` | Studio detail and deploy dialog | Update selection, staged changes, deployment and migration are separate control operations with their existing recovery boundaries. |
| `/api/v1/packwiz/packs/{id}/{mods,datapacks,content,files,datapack-folder}` | Studio detail | Content search/version lookup, mutation, upload and pack-file editing; distinguish control JSON from multipart bytes and preserve path validation. |
| `/api/v1/packwiz/loaders/{loader}/versions` | Studio list/detail | Loader-version catalog control; preserve game-version filter and provider ordering. |
| `/api/v1/staged-config/{server}` and change apply/rollback/delete | Server configuration staged-change controls | Resource-scoped staged lifecycle behavior; reuse staging operations and recovery rules rather than recreating them in a transport adapter. |
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
