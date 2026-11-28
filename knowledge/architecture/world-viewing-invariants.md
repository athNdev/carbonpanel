---
type: Architecture
title: "Map viewing â€” Invariants"
description: "world_view/read never grants observe_players, overlay edits or admin;"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/worldview.proto, internal/worldview]
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
- READY requires the active Overworld config's filename ID in the renderer's
  map list (`/settings.json`) and a scoped rendered tile. The saved-terrain
  preview and viewer persist while BlueMap is unavailable and switch when
  discovery qualifies it. An issued BlueMap key stays bound to its world.
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
- In-context BlueMap install uses ModuleService authorization. A per-controller
  mutex serializes persistence and read-only adoption. The per-server lifecycle
  transition shared with migration holds from config staging through async
  create/start, including failure. During startup, retries can only adopt the
  committed module on its current node assignment. These locks are process-local.
  Consent is explicit; actor and time come from the principal and server clock.
  `startImmediately` needs `modules/start` plus `modules/create`. Checks precede
  world reads and config writes; refusals have no config side effects. The
  managed signal keeps generic Modules-page creation on its existing path.
  Explicit reuse of retained config checks recorded consent and the active Overworld bind without rewriting files.
- Managed BlueMap keeps a read-only rootfs/world bind but mounts a writable panel-owned Mojang resource cache at `/app/bluemap`; without it the renderer exits `Resource-File missing`.
- The managed renderer variant is constructed on the backend from actual world
  facts read via the confined node accessor: private container-only web port
