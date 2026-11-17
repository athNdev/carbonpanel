# ADR 0015: First-class world viewing through an authenticated renderer proxy

Status: Proposed (OSS-346 / D0 decision gate)

## Context

Carbon Panel has no top-level world map. The only renderer representation is
the `builtin-bluemap` module template (`internal/module/builtin_templates.go`),
which publishes a direct `http://host:port` access URL and has no native
authentication. The panel CSP pins framing and connections to the same origin
(`internal/rpc/security_headers.go`). The three product plans
(`product-plans/world-viewing.md`, `integrated-roadmap.md`,
`implementation-checkpoint.md`) identify a first-class world view with honest
degraded states and strict separation of observation from administration.

**D0 interpretation:** the human's "BluePanel" reference is treated as
*BlueMap used alongside a control panel*. No distinct Minecraft product named
BluePanel was found. This is an explicit assumption, not a verified identity;
if a different product was intended the renderer matrix must be re-run.

Primary sources consulted 2026-11-18 (see the plan's §18 for the full list):
BlueMap repository/releases/wiki and BlueMapAPI, squaremap repository/wiki,
Dynmap repository, Overviewer/Mapcrafter. BlueMap v5.29 is MIT, provides 2D and
3D views, three dimensions, live player markers and a marker/shape API, runs as
a Docker sidecar with no native authentication, and uses a periodic region
check (default five minutes) plus file events. squaremap is MIT (Leaflet
BSD-2) and 2D-only; Dynmap is Apache-2.0 and 2D with relief shading. Static
tile generators are offline-only. These are vendor claims from official pages
as displayed on that date, not measured performance or latency results.

## Decision

1. **BlueMap is the default built-in renderer adapter**; squaremap and Dynmap
   are compatibility adapters and statically imported tiles are an offline
   fallback. The existing `builtin-bluemap` template is extended, not replaced.
   Renderer containers stay unprivileged with a read-only world bind and no
   publicly published port (`ProxyEnabled=true`, `ExposePublic=false`).

2. **All renderer traffic goes through an authenticated, same-origin reverse
   proxy.** A browser never receives a renderer origin or port. The proxy path
   is `/api/v1/worldview/map/{key}/...`; `key` is an opaque, short-lived,
   per-user session identifier returned by `GetWorldViewSession`, **not** a
   credential. Each asset request re-authenticates with the existing
   `cp_session` cookie or `Authorization` header via
   `auth.SessionTokenFromRequest`, then enforces `world_view/read` on the
   concrete `server_id`. No token is placed in a URL, no renderer origin is
   exposed to the browser, and the CSP (`frame-ancestors 'self'`,
   `default-src 'self'`) is unchanged. This is the binary/static-content
   exception in ADR 0012, not a new product-data HTTP API.

3. **Local renderer access is node-scoped and fail-closed.** The renderer
   endpoint is resolved from per-node configuration through the injected node
   seams. A remote node without an explicit node-local renderer endpoint is
   reported unavailable; there is never a controller-local fallback and no
   other server's map can be served. Tile and world reads use caller-derived
   contexts; work that outlives a request owns its context and is cancelled and
   waited for on shutdown.

4. **Capabilities are advertised, not assumed.** A renderer adapter reports
   only features verified against its own API. Unsupported features are shown
   in the console as unsupported or unavailable; they are never silently
   substituted with a lookalike control. Live player positions require the
   separate, opt-in `world_view/observe_players` permission and are never
   stored in the panel database by default.

5. **Observation and administration are separate.** `WorldViewService` is
   read-only apart from panel-owned overlays and the opt-in visibility flag.
   Teleport/world/region operations stay on existing `ServerService`,
   `ConfigService` and `FileService` RPCs with their own permissions,
   confirmation and activity-log audit. New RPCs are denied unless explicitly
   mapped; a nil enforcer and anonymous access deny map reads.

6. **Honest state machine.** The workspace renders onboarding → installing →
   render progress → ready → stale/stopped → error/retry. Stale means last-good
   tiles with their real age; "live" is never presented as instantaneous and
   no latency number is claimed without measurement in a real container.

## Consequences

- A renderer sidecar is an additional per-server resource consumer; W15
  admission remains a check, not an atomic reservation, and renderer lifecycle
  must respect it.
- Subpath rewriting and WebGL behavior are renderer-specific and must be
  verified against a real renderer; the adapter boundary keeps that knowledge
  in one place.
- The proxy adds a bounded hop for binary assets; buffering is capped and
  streaming rules from ADR 0013 apply where tiles traverse world storage.
- GPL renderers are imported as static tile *data* only, never linked code;
  legal review is still required before bundling such output.
- `WorldRef` and `Observation` are shared contracts with the player and
  library epics; ADR 0014 (shared product identity) remains the integration
  owner and this ADR keeps its local shapes wire-compatible.

## Verification and evidence limits

Generation uses the pinned Buf plugins from `buf.gen.yaml`. Fail-closed
authorization, proxy scoping, CSP and state transitions are covered by tests at
the registration/proxy seams. No real Minecraft server, renderer container,
browser-rendered live map or measured source-to-UI latency is claimed by this
ADR: renderer API facts are vendor documentation from 2026-11-18 and all
adapter fixtures use local HTTP test servers.
