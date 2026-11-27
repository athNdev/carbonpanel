---
type: Architecture
title: "Map viewing — Rule"
description: "WorldViewService observes worlds; it does not own them. A WorldRef"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/worldview.proto, internal/worldview, internal/bluemapdiscovery, internal/rpc/services/module_bluemap.go, web/carbon-panel/src/routes/worlds, web/carbon-panel/src/lib/components/worldview]
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

The server preview and World tab share a map dialog. Loading labels identify the renderer phase. The built-in preview omits a routine badge and the dialog dates the newest visible chunk save when known.
The compass respects browser and panel reduced-motion preferences. Command feedback stays with Administration.
Without an explicit level, scoped GetWorldViewStatus resolves the active map. Standalone links still check only map
access. The catalogue requires a global read grant. Setup and unavailable
states do not mount an iframe.
A failed refresh keeps a valid last-known map and reports the error. The page
renews both built-in and renderer sessions before their deadline, hides expired
maps and retries temporary failures while the scope remains authorized. Read
denials and scope changes clear the current map session. Renderer changes
acquire a matching session before mounting the replacement map.

Native terrain uses bounded workers and a scoped map session. Inventory can
defer format checks. Lifecycle operations temporarily refuse reads. The
viewport aborts tiles outside its current session, dimension or viewport.
It identifies empty and partial saves. See [native transport](../decisions/native-worldview-transport.md)
for dimension eligibility and coordinate links. Non-sensitive point markers
use scoped ListMapOverlays results for the selected dimension, and their list
can center offscreen points. The native map also offers managed BlueMap setup
with the existing authorization and consent checks. Shared links carry
coordinates and require the recipient’s scoped read permission. Native World
View never changes the save.

Renderer traffic stays same-origin with short-lived keys and an AuthStore session
cookie renewed before each map session. Each asset read rechecks the server’s
map-read permission. Renderer endpoints stay private. Endpoint and renderer
failures have distinct states. Authorized commands remain available on saved
terrain or when the renderer is unavailable.

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
