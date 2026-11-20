---
type: Architecture
title: "Module system — Convergence (part 2)"
description: "Parent deletion has an explicit child policy. A full delete stops and"
status: stable
owners: [oss-runtime]
code_refs: [internal/module, internal/packwiz, internal/indexers, internal/blueprint, internal/blueprint/resolve.go, internal/module/hooks.go, internal/module/reconcile.go, internal/docker/client.go, internal/reconciler, internal/rpc/services/server.go, web/carbon-panel/src/lib/module-identity.ts, web/carbon-panel/src/routes/modules/ActiveModules.svelte, web/carbon-panel/src/lib/components/server-ui/ServerModules.svelte, web/carbon-panel/src/routes/servers/new/+page.svelte]
sources:
  - title: Parent concept
    resource: module-system.md
---
Parent deletion has an explicit child policy. A full delete stops and
removes sidecar containers before the server row. `DeleteServer` with
`keep_data` drops module panel records through `ForgetModule` and leaves
the containers in place, because `RemoveContainer` always sets
`RemoveVolumes`. Detach skips child cleanup so sidecars keep running with
the detached server. After a parent migration, module rows follow the
target node and clear their container ids; source sidecar containers are
not removed.
