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
server when the active level is unknown. The scoped fallback uses
GetWorldViewStatus with the concrete server; the all-server catalogue
requires a global read grant. World View uses names from the authorized
server inventory. Setup and unavailable states do not mount a map.
A failed refresh keeps a valid last-known map and reports the error. The page
renews both built-in and renderer sessions before their deadline, hides expired
maps and retries temporary failures while the scope remains authorized. Read
denials and scope changes clear the current map session.

The native viewport reads saved Overworld terrain through its map session.
It bounds visible tile requests and aborts work outside the current session
or viewport. Empty and partial saves are explicit; controls move the map view
and do not modify the world. Shared map links carry coordinates only and
still require the recipient’s scoped read permission.
The native Overworld map projects existing non-sensitive markers, waypoints,
and points of interest from the scoped ListMapOverlays result. The marker
list can center offscreen points. Native markers include neither live player
locations nor region overlays.

Renderer traffic is same-origin only. The browser receives an opaque,
short-lived session key, never a renderer URL or port; every proxied asset
request re-authenticates with the cookie or bearer token and enforces
`world_view/read` on the concrete server. Missing renderer endpoints, remote
nodes without a node-local renderer, unsupported capabilities and stale tiles
are reported honestly; there is no public map endpoint, no controller-local
fallback and no cross-origin iframe.
