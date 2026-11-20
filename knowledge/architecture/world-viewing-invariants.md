---
type: Architecture
title: "Map viewing — Invariants"
description: "world_view/read never grants observe_players, overlay edits or admin;"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/worldview.proto, internal/worldview, web/carbon-panel/src/routes/worlds]
sources:
  - title: Parent concept
    resource: world-viewing.md
---
# Invariants

- `world_view/read` never grants `observe_players`, overlay edits or admin;
  anonymous and nil-enforcer requests are denied.
- Renderer capabilities are advertised by the adapter and gate visible
  controls; unsupported means unsupported, not a substitute control.
- Freshness is an `Observation`: timestamp, source and confidence. A missing
  renderer is onboarding, a stopped renderer is stale, not blank or "live".
- Container privileges do not change; renderer world mounts stay read-only.
- Explicit `world_view.endpoints` always win. An installed `builtin-bluemap`
  module is discovered only on a local node through its exact server/node/module
  identity, verified container metadata (labels, pinned image, owned-network IP
  and membership, effective read-only level bind) and the confined node data
  accessor. The renderer is dialed at the inspected IP, not a Docker DNS alias
  (host/native modes cannot resolve container names); no port probing,
  host-port binding, public listener or cross-server fallback. No HTTP is sent
  when a runtime gate fails, even using a previously issued map session.
- A listening web server is not a configured or rendered world. READY requires
  the exact target world in the module config, a rendered scoped tile and the
  renderer's own map list (`/settings.json`); missing facts report setup,
  rendering, stopped, unreachable or error with a next action.
- Both discovered and explicit BlueMap adapters require a bounded JSON
  textures document for every map listed by the renderer before reporting
  READY. The health check and proxy both retry the exact `.gz` sibling on a
  plain-path 404. Missing or invalid textures leave the map session unavailable
  and surface an actionable error for a discovered module.
- BlueMap web assets keep the panel CSP unchanged. The proxy forwards
  `Accept-Encoding`/`Content-Encoding` for every renderer; only for BlueMap map
  `textures.json`/tile payloads it retries exactly the `.gz` sibling of a 404
  and serves literal `.gz` client-decompression paths with the uncompressed
  media type and no `Content-Encoding`. Ranged requests, other renderers and
  other paths keep their prior behavior; session/auth/scope and private
  no-store cache gates are unchanged. Compatibility with `script-src` comes
  from the pinned BlueMap v5.29 JIT webapp rebuild, not from `unsafe-eval`, a
  per-route CSP, or a capture-only bypass. See
  [ADR 0048](../../docs/adr/0048-bluemap-asset-routing-and-csp.md).
- In-context BlueMap install uses existing ModuleService authorization. It is a
  server-side singleton per server guarded by a controller-PROCESS mutex held
  through persistence: it covers concurrent RPCs/tabs/operators/retries on ONE
  controller process and makes no cross-controller or distributed guarantee.
  Consent is explicit but its actor/time come from the authenticated principal
  and server clock. An existing module is adopted read-only. `startImmediately`
  needs `modules/start` as well as `modules/create`; both are checked before any
  world read or config write, and refusals leave zero config side effects. The
  managed signal isolates this workflow so generic Modules-page creation still
  works without it.
- Managed BlueMap keeps a read-only rootfs/world bind but mounts a writable panel-owned Mojang resource cache at `/app/bluemap`; without it the renderer exits `Resource-File missing`.
- The managed renderer variant is constructed on the backend from actual world
  facts read via the confined node accessor: private container-only web port
