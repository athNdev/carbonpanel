# World viewing runbook

World viewing is an **observation-only** surface. `WorldViewService` reports
worlds, renderer health and panel-owned overlays; it never starts, stops,
modifies or teleports anything. The governing decisions are in
[ADR 0015](adr/0015-world-viewing-renderer-proxy.md) and the durable rule is in
`knowledge/architecture/world-viewing.md`.

## 1. Renderer lifecycle

BlueMap is the built-in default adapter (`builtin-bluemap` module template);
squaremap and Dynmap are compatibility adapters and `STATIC` is an offline tile
snapshot.

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

No renderer is discovered by probing ports. The renderer topology is configured
in `config.yaml` under `world_view` (see `config.example.yaml`) and injected at
the composition root as `worldview.Config`; the shipped default is empty, so
every world starts `ABSENT`. An explicit endpoint is required before any map
becomes available.

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
the server.

## 3. Capability matrix

Capabilities come from the adapter and gate visible controls. Unsupported means
unsupported — no lookalike control is substituted.

| Capability | BlueMap | squaremap | Dynmap | STATIC |
| --- | --- | --- | --- | --- |
| `map.2d` | yes | yes | yes | static tiles only |
| `map.3d` | yes | no | no | no |
| `dimensions` | yes | not verified | not verified | no |
| `markers` | yes | not verified | not verified | no |
| `players` | only with explicit bridge feed | no | no | no (offline) |
| `time_weather` | no | no | no | no |
| `telemetry` | no | no | no | no |
| `static` | no | no | no | yes |

Live player positions additionally require `world_view/observe_players` and are
audited as `world_view.observe_players`. Panel overlays require
`world_view/update` and are audited as `world_view.overlay.set`. Sensitive
overlays are hidden from readers without `world_view/observe_players`. Player
locations are not persisted to the panel database by this service.

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

## 6. Evidence limits

All tests in this change use local `httptest` renderer fixtures and in-memory
fakes; test names are marked `Fixture`. They verify authorization, scoping,
state projection, audit and proxy transport behavior. No live Minecraft server,
renderer container, WebGL/browser-rendered map, real player feed or measured
source-to-UI latency is claimed. Renderer feature facts come from the vendor
documentation summarized in ADR 0015 (consulted 2026-11-18), not from local
measurement. Capabilities that cannot be verified are reported unsupported.
