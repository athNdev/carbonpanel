# ADR 0040: Installed BlueMap discovery, consent and in-context install

Status: Accepted (OSS-388; extends ADR 0015 and ADR 0031)

## Context

World View reported `ABSENT` for servers with a running `builtin-bluemap`
module: `worldview.Resolver` consulted only explicit `world_view.endpoints`,
whose shipped default is empty. The live evidence was two running BlueMap
containers logging `Webserver started` on container port 8100, with NULL host
port bindings, read-only `/app/world` mounts, and a shared `carbon-panel-network`.
Upstream documentation confirms that a listening web server does not imply a
configured or rendered world, and that the Docker image requires a standalone
map config per target world plus explicit `accept-download` acknowledgement.

## Decision

1. **Explicit operator endpoints always win.** `Resolver` checks
   `world_view.endpoints` first; discovery is consulted only on a miss. No port
   probing, arbitrary URL, public renderer listener, host-port dependency or
   cross-server fallback is introduced. A remote node has no private
   owned-network path and stays unavailable without an explicit, allowlisted
   node-local endpoint.

2. **Discovery is identity-bound and private.** `ModuleDiscovery` locates the
   exact server/node/template module from panel-owned state, then verifies the
   inspected owned-network **IP** (not a Docker DNS alias) — host-network and
   native-binary deployments cannot resolve container names, so the private
   base URL is built from the container address observed by the node-scoped
   inspect; an empty/invalid IP fails closed.
   node-scoped container metadata: module/server/node/template labels, the
   pinned image (`v5.29`), unprivileged configuration, owned-network membership
   and the effective read-only `/app/world` bind whose source is the server's
   ACTUAL level folder. Config and tile assets are read only through the
   confined node data accessor with bounded reads. No HTTP request is made
   until every runtime gate passes. A configured map and rendered tiles are
   required in addition to a reachable renderer; readiness is never derived
   from an HTTP 200 on `/`. Explicit BlueMap endpoints likewise require a
   non-empty `/settings.json` map list before reporting `READY`.

3. **Install goes through the existing ModuleService.** The in-context Install
   BlueMap action calls `CreateModule`/`StartModule` with a managed-action
   metadata signal. The managed action is a server-side singleton guarded by a
   **controller-process mutex** (`blueMapInstallMu`) held from the singleton
   check until the module row is persisted. This covers one controller process
   (one RPC server instance / one OS process) and the concurrent RPCs,
   tabs, operators and retries it serves. It explicitly does **not** provide a
   cross-controller, cross-process or distributed guarantee: two controllers
   sharing one database could both create a module. Cross-process atomicity is
   out of scope (ADR 0011/0015); operators must not run two controllers against
   one database for managed BlueMap installs. All callers on one controller
   converge on one module and receive that module. An existing module is adopted
   read-only: no create, start, reconfigure, restart or config overwrite.
   Consent acceptance is an explicit operator action; the actor and time are
   derived server-side from the authenticated principal and clock.
   `startImmediately` requires `modules/start` in addition to `modules/create`.

   The managed signal isolates this workflow from the generic Modules-page
   action: existing operators are not required to supply the new consent
   metadata and their create flow is unchanged. Both entry points still go
   through the same ModuleService authorization and operation journal.
   **Ordering:** `modules/create`, and `modules/start` when `startImmediately`
   is set, are both checked BEFORE any world read, config staging or DB write.
   Refusals have zero config side effects.

4. **Managed variant is constructed on the backend.** Client-supplied ports,
   volumes, command, init command, env overrides, config and access URLs are
   discarded for the managed action. The server builds a private container-only
   web port (negative host-port sentinel: no allocation, no publish), the
   read-only `/app/world` bind whose source is the server's ACTUAL canonical
   level folder, writable config/data/web binds, the pinned `-r -u -w` command
   and a deterministic per-dimension BlueMap config. Eligibility reads the real
   world via the confined node data accessor (Java Edition level.dat, standard
   and custom dimensions) and returns concrete refusal reasons. No fixed
   client-side `sed` init command is used; the panel writes the consent and map
   configs from backend state. Config files are STAGED inside the confined data
   root and committed only after the module row persists; a persistence failure
   rolls staging back, an identical retry is a no-op, and a pre-existing
   operator config is preserved verbatim (never overwritten), including a target
   appearing between staging and promotion (no-clobber re-check at commit).
   Config-commit failure rolls back staged files and deletes the just-created
   module row while the singleton lock is still held, using a fresh bounded
   cleanup context; a later retry succeeds cleanly. The private web port uses a
   `-1` host-port sentinel that every allocation, availability check, Docker
   publish and reconcile helper skips (`HostPort > 0`).

5. **Honest states and next actions.** Setup, starting, stopped, unreachable and
   ready states are projected with actionable messages. Created-but-failed-start
   is not reported as generic success: the module status and metadata carry the
   starting state and next action, and World View reflects the module's error
   state. This in-process mutex is not a cross-process atomic reservation
   (ADR 0011/0015).

6. **Renderer topology stays safe.** The template pins the renderer version,
   derives the world mount from the actual level name, publishes no renderer
   origin to the browser, and no longer silently flips `accept-download`.
   Old manual adapters and explicit endpoints remain intact.

## Consequences

- Private discovery is local-node only; remote nodes are labelled
  unsupported/manual with the exact next action (explicit node-local endpoint).
- A configured-but-unrendered world reports a setup/rendering state, not a map.
- The singleton guarantee is per controller process; operators must not run two
  controllers against one database for BlueMap installs.
- Readiness is composed from identity, config and asset facts, so a stale or
  wrong renderer can never be presented as this server's live map.

## Evidence limits

Tests use owned config fixtures, in-memory module registries and local HTTP
responders; no live BlueMap container, browser-rendered map or measured latency
is claimed. Runtime container metadata is verified through the existing
node-scoped Docker client seam; no live daemon was mutated. A real disposable
BlueMap container boot / pinned-image / read-only-mount / config-and-tiles
initialization journey and an owned-browser end-to-end run remain unverified:
this environment has no reachable Docker daemon (`docker info` cannot connect)
and live WSL mutation is out of scope. Do not read the fixtures as live evidence.
