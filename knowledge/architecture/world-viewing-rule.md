---
type: Architecture
title: "Map viewing — Rule"
description: "WorldViewService observes worlds; it does not own them. A WorldRef"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/worldview.proto, internal/worldview, web/carbon-panel/src/routes/worlds]
sources:
  - title: Parent concept
    resource: world-viewing.md
---
# Rule

`WorldViewService` observes worlds; it does not own them. A `WorldRef`
identifies a library binding, a server-local level or a static tile snapshot —
a folder name is never an identity. Map overlays and the opt-in player
visibility flag are the only mutations, and both are audited. Teleport and
world/region operations stay on the existing server, config and file RPCs with
their own authorization.

The server World tab links to the active map, or to World View scoped to that
server when the active level is unknown. World View uses server names only
from the authorized server inventory. Setup and unavailable states do not
mount a map.

Renderer traffic is same-origin only. The browser receives an opaque,
short-lived session key, never a renderer URL or port; every proxied asset
request re-authenticates with the cookie or bearer token and enforces
`world_view/read` on the concrete server. Missing renderer endpoints, remote
nodes without a node-local renderer, unsupported capabilities and stale tiles
are reported honestly; there is no public map endpoint, no controller-local
fallback and no cross-origin iframe.
