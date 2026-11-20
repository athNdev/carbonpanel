---
type: Architecture
title: "Module system — Shape + Rules"
description: "Modules are server-side content units. Hooks run inside the server's runtime;"
status: stable
owners: [oss-runtime]
code_refs: [internal/module, internal/packwiz, internal/indexers, internal/blueprint, internal/blueprint/resolve.go, internal/module/hooks.go, internal/module/reconcile.go, internal/docker/client.go, internal/reconciler, internal/rpc/services/server.go, web/carbon-panel/src/lib/module-identity.ts, web/carbon-panel/src/routes/modules/ActiveModules.svelte, web/carbon-panel/src/lib/components/server-ui/ServerModules.svelte, web/carbon-panel/src/routes/servers/new/+page.svelte]
sources:
  - title: Parent concept
    resource: module-system.md
  - title: Configure a new server
    resource: ../../docs/carbon-panel/src/content/docs/guides/server-setup.mdx
---
# Shape

Modules are server-side content units. Hooks run inside the server's runtime;
packwiz and the indexers resolve CurseForge and Modrinth content; blueprints
describe a server's desired configuration.

# Rules

- Every module operation must use the Docker client resolved for the target
  server. Do not use the manager's client directly.
- Surface indexer and delivery failures; do not turn a partial download into a
  successful install.
- Verify checksums before declaring a modpack delivered.
- Provider credentials must not accompany keyless fallbacks or cross-origin
  redirects. The CurseForge adapter owns fallback policy; see
  [provider credentials](../decisions/provider-http-policy.md).
- A hook failure must be attributable to the module and the server.
- Create Server attaches selected modules stopped. Mod and datapack failures
  stay visible on the created server's setup results and hold a requested first
  start until the selected content has been added.
- The Resource Pack Host serves a dedicated read-only folder. Docker prepares
  explicitly requested bind sources within mapped, symlink-checked safe roots.
- Create-path blueprints are Java/tag seeds resolved to image, env and
  protocol. A Bedrock full-image seed is labeled unsupported and is not a
  native edition path. See [blueprint create qualification](../decisions/blueprint-create-qualification.md).
