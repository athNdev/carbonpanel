---
type: Architecture
title: "Map viewing — Rule"
description: "WorldViewService observes worlds; it does not own them. A WorldRef"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/worldview.proto, internal/worldview, internal/bluemapdiscovery, internal/rpc/services/module_bluemap.go, web/carbon-panel/src/routes/worlds]
sources:
  - title: Parent concept
    resource: world-viewing.md
  - title: Installed BlueMap discovery
    resource: ../../docs/adr/0040-bluemap-installed-module-discovery.md
---
# Rule

`WorldViewService` observes worlds; it does not own them. A `WorldRef`
identifies a library binding, a server-local level or a static tile snapshot —
a folder name is never an identity. Map overlays and the opt-in player
visibility flag are the only mutations, and both are audited. Teleport and
world/region operations stay on the existing server, config and file RPCs with
their own authorization.

Renderer traffic is same-origin only. The browser receives an opaque,
short-lived session key, never a renderer URL or port; every proxied asset
request re-authenticates with the cookie or bearer token and enforces
`world_view/read` on the concrete server. Missing renderer endpoints, remote
nodes without a node-local renderer, unsupported capabilities and stale tiles
are reported honestly; there is no public map endpoint, no controller-local
fallback and no cross-origin iframe.

Managed BlueMap install and private discovery share one node eligibility
check. It accepts legacy controller aliases and enabled registered nodes
marked local in persisted state. Discovery uses the controller Docker client,
checks the server's current node assignment and opens only its confined storage.
Managed startup holds the server transition through its bounded Docker call
and terminal state write. Manual Start, Restart and Recreate share that lease
and recheck the node assignment. A canceled post-create write records the
known container ID with its stopped state so a later Start can reuse it.
