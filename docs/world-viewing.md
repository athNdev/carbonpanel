# World viewing runbook

World viewing is an **observation-only** surface. `WorldViewService` reports
worlds, renderer health and panel-owned overlays; it never starts, stops,
modifies or teleports anything. The governing decisions are in
[ADR 0015](adr/0015-world-viewing-renderer-proxy.md) and the durable rule is in
`knowledge/architecture/world-viewing.md`.

Open a server and expand its **World View** card to explore the map. The
server’s **World** tab opens the same viewer through **Open map**. The card
shows either a renderer preview or sampled saved terrain. Previously shared
map links continue to open a standalone viewer for recipients with map access.
If the active level is still unknown, Carbon Panel checks for
the active map using that server’s World View read permission. A [built-in
terrain map](world-terrain.md) opens supported local Java saves without a
module. Choose **Set up a 3D map with BlueMap** from a built-in map, or
**Install BlueMap** from the renderer setup screen, when the account and node
support it. The managed sidecar renders textured buildings after Minecraft
saves them and BlueMap processes the changed chunks. It has no player-location
feed. **Map ready** means the renderer can serve a map.

Map sessions renew automatically. During a temporary connection failure the
page keeps an authorized map available until its session expires, then retries.

The native saved-terrain map shows saved markers, waypoints and points of
interest for the selected map dimension. Select a pin for its coordinates.
Open **Map markers** to center the map on a point or toggle pin visibility.
Qualified local Vanilla Java saves offer **The End** in **Map dimension**.
**Copy map link** includes the selected dimension when sharing End coordinates.
Live player positions and regions are outside the native map’s coverage.

## 1. Renderer lifecycle

BlueMap is the optional managed renderer (`builtin-bluemap` module template).
Squaremap and Dynmap are compatibility adapters. The static adapter displays
an offline tile snapshot.

- **Install** a renderer through the existing module workflow
  (`ModuleService.CreateModule` / `StartModule`) using the built-in template or
  a module that exposes a web root. Keep the renderer container unprivileged,
  mount world data read-only where the image supports it, and keep
  `ProxyEnabled=true`, `ExposePublic=false`. Do **not** publish a renderer port
  on the host or through the proxy listener.
- **Update** via the normal module version/recreate flow. A failed update must
  leave the previous module in place; the world view reports `ERROR` or `STALE`
  rather than claiming a fresh render.
- **Remove** via `ModuleService.DeleteModule`. Removing a renderer does not
  delete the world or its database. The world view then reports `ABSENT` with an
  onboarding message.

Explicit `world_view.endpoints` in `config.yaml` always take precedence; an
operator endpoint is the trust decision described in ADR 0015. When no endpoint
is configured, an installed `builtin-bluemap` module is discovered on a **local
node** through the private owned network (ADR 0040): the exact server/node/module
identity and node-scoped container metadata (labels, pinned image, owned-network
membership and the effective read-only bind of the server's actual level folder)
must verify before any HTTP request. Module config and tile assets are read only
through the confined node data accessor. This includes an enabled Docker node
registered as local to the controller. It can use managed BlueMap installation
and private discovery with its registered ID and the configured controller
Docker client. Unknown or disabled registered nodes cannot use the private path.
A remote node needs an explicit, allowlisted node-local endpoint.
No renderer is discovered by port probing or host-port bindings.

A discovered module reaches READY after the renderer's `/settings.json` lists
the configured Overworld map for the server's actual level and the node data
accessor finds a scoped tile. The map ID comes from its configuration filename,
as in BlueMap's generated `overworld.conf`.
Both discovered modules and explicit BlueMap endpoints
also need to serve textures for every map they list, including secondary
dimensions. A discovered renderer with unavailable textures shows an error
and a retry action in World View. When a discovered module is still starting,
rendering, stopped or unavailable, World View shows the built-in saved-terrain
map if the world supports it, with the module's current condition. The server
preview and full viewer switch automatically after the required checks pass.

For an existing BlueMap module, use the server's **Modules** tab to inspect
its logs and current volume settings:

- If the logs report a missing resource download, review the Minecraft EULA
  and BlueMap's resource download terms. If you accept both, edit
  `modules/bluemap/config/core.conf` from the server's **Files** tab and set
  `accept-download: true`.
- If the `/app/world` volume points to the server data root, edit BlueMap's
  **Volumes** and set its host source to
  `{{server.data_path}}/<active-level-folder>` with **Read Only** enabled. Use
  the actual folder containing `level.dat`.
- For older BlueMap modules without a writable resource cache, add a
  **Volumes** entry from `{{server.data_path}}/modules/bluemap/resources`
  to `/app/bluemap` with **Create Directory** enabled. The current template
  supplies this mount for new Modules-tab installs.
- Confirm that `modules/bluemap/config/maps/overworld.conf` (or an equivalent
  Overworld map config) points to the mounted world at `/app/world`. Apply
  volume changes through the module's existing save/recreate flow, then start
  or restart the module. World View will pick it up after its first render.

The in-context **Install BlueMap** action uses the existing
ModuleService create/start RPCs; it is a server-side singleton per server,
records explicit operator consent (actor and time from the authenticated
principal and clock), adopts an existing module without mutation, and requires
`modules/start` in addition to `modules/create` when starting immediately.
If the module record is gone and its files remain, **Use existing configuration**
registers and starts a new private BlueMap container with those files. The
existing configuration needs a single `accept-download: true` entry in
`modules/bluemap/config/core.conf` and an Overworld map pointing to
`/app/world` (the stock relative value `world` also works). Review the
download terms before changing the acknowledgement. Existing files are reused
without a rewrite. Use **Open Modules** to inspect or restart an installed renderer.
Docker image preparation, container creation and start share a two-minute
startup limit. If the limit expires, the panel releases the server lifecycle
transition and marks the module as errored. Open Modules and retry Start;
the panel keeps the container ID when Docker already returned it.

## 2. Endpoint topology and the same-origin proxy

`world_view.endpoints` holds explicit entries (`node_id`, optional `server_id`,
`kind`, `host`, `port`, `base_path`, `health_path`, `player_feed`) and
`world_view.allowed_hosts` is the remote-node allowlist. These map directly to
`worldview.Config` at the composition root; unknown renderer kinds degrade to
`UNSUPPORTED` rather than being guessed.

- A **local node** endpoint must use loopback (`127.0.0.1` or `::1`). A LAN or
  public bind is rejected as an invalid endpoint.
- A **remote node** must have its own node-local endpoint with a host the
  operator allowlisted. Loopback is rejected for a remote node because it would
  address the controller, not the node. There is no controller-local fallback
  and no other server's renderer can serve a request.
- Missing/invalid endpoint, remote node without an endpoint, or a node
  classification error yields `ABSENT`/`unavailable` and an HTTP 503 at the
  proxy with **no** upstream request.

The browser only ever receives the same-origin path
`/api/v1/worldview/map/{key}/...` from `GetWorldViewSession`. The key is an
opaque random identifier (about five minutes), bound to the issuing user ID,
server ID and canonical world reference. It is **not** a credential and is
never a token in a URL: every asset request re-authenticates with the
`cp_session` cookie or an `Authorization` bearer token, then re-checks
`world_view/read` on the concrete server (including subuser grants). Keys for
another user, another server, or that have expired are denied with 403 before
any renderer contact.

Proxy details: GET/HEAD only, WebSocket upgrades rejected (panel `/ws` is the
only upgrade route), bounded at 8 MiB per asset, fixed-size streaming buffer,
client timeout, hop-by-hop and renderer-defined headers stripped,
`X-Content-Type-Options: nosniff` set. CSP is unchanged; no new security
headers or CORS allowances are added. Renderer origins and ports never leave
the server. Renderer redirects are never followed: a 3xx from a renderer is
returned as a 502, so an allowlisted node-local endpoint cannot be turned into
an external or controller-local request under the panel origin.

### Renderer web assets and CSP compatibility

BlueMap v5.29 stores map data gzipped (`textures.json.gz`, `*.prbm.gz`) and its
own webserver implements two routes: a plain path answered with
`Content-Encoding: gzip` when the browser accepts gzip, and a literal `.gz`
path decompressed by the webapp itself. The proxy forwards `Accept-Encoding`
and `Content-Encoding` end to end for every renderer. Only for the BlueMap
adapter, and only for its map `textures.json` and PRBM/PNG tile payloads, does
it also retry exactly the `+".gz"` sibling when a plain path 404s. The
uncompressed media type is set from an explicit table (`.json` →
`application/json`, `.prbm` → `application/octet-stream`); a literal `.gz`
request gets no `Content-Encoding`. Ranged requests are passed through without
representation switching, and the session/auth/scope and private no-store
cache gates are unchanged. Other renderers, other asset paths, other encodings
and in-proxy decompression are not introduced. A missing tile or texture stays
a 404, and the adapter does not report READY (nor issue a map session) unless
`/maps/{id}/textures.json` answers — through the plain encoded route or the
exact `.gz` sibling — with a bounded JSON textures document for every
configured map. A visible iframe or canvas is never used as evidence that a
map loaded.

The panel CSP is unchanged: no `unsafe-eval`, no new `unsafe-inline` and no
capture-only bypass. Compatibility comes from a pinned rebuild of upstream
BlueMap `v5.29` source commit `a61e143fe1e069c536159e0bc79410d5b6da3885` with
`__INTLIFY_JIT_COMPILATION__: true` added to `common/webapp/vite.config.js`
`define`. The verified JIT bundle `index-BA9KJ4pi.js` has SHA-256
`ba940f930de0642acfd2ca8b0eda6aa0914ce02fcf673cee18262b3dd5217da3` and no
`new Function` / `compileToFunction` occurrences; the pristine bundle contains
both. See [ADR 0048](adr/0048-bluemap-asset-routing-and-csp.md) for provenance
and evidence limits. The review stack applies this rebuild before opening the
map. Managed install currently starts the upstream image with its stock webapp,
which can fail to render under the panel CSP.

For disposable review stacks, prefer BlueMap's own webserver and verify with
`curl -H 'Accept-Encoding: gzip' .../maps/world/textures.json` from the panel's
network namespace before capture. If a plain static root is unavoidable, use
the proxy route above and assert a non-2xx texture/tile response fails capture
rather than accepting a black canvas. Do not edit capture code to relax either
check.

### Iframe trust assumption and residual risk

The embed is same-origin and carries
`sandbox="allow-scripts allow-same-origin allow-forms allow-popups"`. The
combination of `allow-scripts` and `allow-same-origin` means renderer scripts
**share the panel origin**: the sandbox does not isolate a hostile renderer
from the console. Treat any configured renderer as trusted code. The residual
risk sits in operator configuration — a compromised renderer container or a
malicious allowlisted remote-node host can execute same-origin script. Endpoints
are therefore operator-owned, fail-closed, validated as loopback on local nodes
or explicitly allowlisted node-local hosts on remote nodes, never guessed by
port probing, and cannot be supplied by an authenticated or anonymous user.
Do not point a renderer at untrusted content and do not relax container
privileges to make one work. Remove `allow-popups` for a renderer that does not
need it.

## 3. Capability matrix

Capabilities come from the adapter and gate visible controls. Unsupported means
unsupported — no lookalike control is substituted. The built-in viewport
additionally uses its authorized manifest to decide which saved dimensions it
can open.

| Capability | Built-in terrain | BlueMap | squaremap | Dynmap | Static |
| --- | --- | --- | --- | --- | --- |
| `map.2d` | saved Java terrain | yes | yes | yes | static tiles only |
| `map.3d` | no | yes | no | no | no |
| `dimensions` | Overworld and qualified local Vanilla Java End | yes | not verified | not verified | no |
| `markers` | saved points in the selected dimension | yes | not verified | not verified | no |
| `players` | no | only with explicit bridge feed | no | no | no (offline) |
| `time_weather` | no | no | no | no | no |
| `telemetry` | no | no | no | no | no |
| `static` | no | no | no | no | yes |

Live player positions additionally require `world_view/observe_players`. The
console toggle is **UI consent**: it is hidden by default, requires that
permission, and calls the audited `SetPlayerVisibility` RPC which writes a
`world_view.observe_players` activity event. The security boundary is the
permission check, not the click: a direct RPC caller that already holds
`world_view/observe_players` can set the flag without having used the UI, and
the audit event records the call rather than proving a user saw or confirmed a
dialog. Do not treat the UI toggle as a second authorization factor.

Panel overlays require `world_view/update` and are audited as
`world_view.overlay.set`. Sensitive overlays are hidden from readers without
`world_view/observe_players`. Player locations are not persisted to the panel
database by this service.

## 4. Administration stays on existing RPCs

There are no new teleport/world/region RPCs and a map session key authorizes
**none** of them:

- **Teleport / server commands** → `ServerService.SendCommand` (`servers/command`).
- **World or region configuration** → `ConfigService.UpdateServerConfig`
  (`server_config/update`).
- **World/region files** → `FileService` RPCs (`files/*`).

Those operations keep their own scopes, confirmation flows and activity audit.
A `world_view` grant, an overlay edit, or a map proxy key must never be treated
as authorization for them.

## 5. RBAC summary

`world_view` is server-scoped (`world_view -> servers`) with actions `read`,
`observe_players`, `update` and `admin`.

| Procedure | Required permission | Scope |
| --- | --- | --- |
| `ListWorldViewTargets` | `world_view/read` | global `*` (filtered per server in the service) |
| `GetWorldViewStatus` | `world_view/read` | `server_id` |
| `GetWorldViewSession` | `world_view/read` | `server_id` |
| `ListCapabilities` | `world_view/read` | `server_id` |
| `ListMapOverlays` | `world_view/read` | `server_id` |
| `SetMapOverlay` | `world_view/update` | `server_id` |
| `SetPlayerVisibility` | `world_view/observe_players` | `server_id` |

Default grants: `user` gets `world_view/read` only; `anonymous` gets nothing;
`observe_players`, `update` and `admin` are admin-only (admin matches `*`).
Unmapped procedures and a nil enforcer are denied by the RPC interceptor.

## 6. Explicit limitations and deferred surfaces

- **Renderer-owned camera:** pan, zoom and coordinate search are provided by
  the renderer's own controls inside the same-origin embed. No renderer camera
  API has been verified, so the panel deliberately exposes no simulated
  pan/zoom buttons or coordinate "go" action. Coordinates in a URL are intent
  for player/admin deep links, not a claimed map camera.
- **Static snapshots:** a `static` renderer honestly reports `STALE` with its
  source and confidence, but no tile proxy is implemented for imported snapshot
  files. It therefore exposes freshness information without an iframe rather
  than a blank or broken map.
- **Subuser discovery:** The all-server catalogue uses
  `ListWorldViewTargets` and requires an unscoped `world_view/read` grant.
  A server-scoped subuser can follow a map deep link or use **Open map** from
  the server’s **World** tab. The fallback page reads only that server’s active
  map through `GetWorldViewStatus`.
- **Sibling epics:** the shell links to `/players` and `/library`, which are
  owned by the player-management and World Library epics. The world-viewing PR
  is a cumulative draft and is expected to be integrated with those routes;
  it does not stub or imitate them.

## 7. Evidence limits

All tests in this change use local `httptest` renderer fixtures and in-memory
fakes; test names are marked `Fixture`. They verify authorization, scoping,
state projection, audit and proxy transport behavior. No live Minecraft server,
renderer container, WebGL/browser-rendered map, real player feed or measured
source-to-UI latency is claimed. Renderer feature facts come from the vendor
documentation summarized in ADR 0015 (consulted 2026-11-18), not from local
measurement. Capabilities that cannot be verified are reported unsupported.
