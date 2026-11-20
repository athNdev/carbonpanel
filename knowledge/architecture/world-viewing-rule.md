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

The server World tab links to the active map, or to World View scoped to that
server when the active level is unknown. The scoped fallback uses
GetWorldViewStatus with the concrete server; the all-server catalogue
requires a global read grant. World View uses names from the authorized
server inventory. Setup and unavailable states do not mount a map.
A failed refresh keeps a valid last-known map and reports the error. The page
renews both built-in and renderer sessions before their deadline, hides expired
maps and retries temporary failures while the scope remains authorized. Read
denials and scope changes clear the current map session. Renderer changes
acquire a matching session before mounting the replacement map.

The native viewport reads saved Overworld terrain through its map session.
Format checks and rendered tiles share a bounded worker pool. Inventory can
defer a format check without hiding the world's link. Active lifecycle
operations temporarily refuse new map reads.
The viewport bounds visible tile requests and aborts work outside the current session
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

Managed BlueMap install and private discovery share one node eligibility
check. It accepts legacy controller aliases and enabled registered nodes
marked local in persisted state. Discovery uses the controller Docker client,
checks the server's current node assignment and opens only its confined storage.
Managed startup holds the server transition through its bounded Docker call
and terminal state write. Manual Start, Restart, Recreate and managed updates
share that lease and recheck node eligibility. Async recreation keeps the lease.
Only the guarded installer sets the persisted managed marker. Module metadata
updates preserve that decision. A canceled post-create write records the known
container ID and stopped state so a later Start can reuse it.
