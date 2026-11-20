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
   inspect; an empty/invalid IP fails closed. Discovery also verifies
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

- Managed install and private discovery share the same local-node check.
  Legacy controller aliases and enabled registered nodes marked local by the
  persisted node record qualify. Private discovery inspects the configured
  controller daemon and verifies the server's current assignment against the
  module and Docker labels before reading its data.
  Missing and disabled nodes have no private discovery. Remote nodes need an
  explicit, allowlisted node-local endpoint.
- A configured-but-unrendered world reports a setup/rendering state, not a map.
- The singleton guarantee is per controller process; operators must not run two
  controllers against one database for BlueMap installs.
- Readiness is composed from identity, config and asset facts, so a stale or
  wrong renderer can never be presented as this server's live map.

## Evidence limits

Unit tests use owned config fixtures, in-memory module registries and local
HTTP responders. The disposable live-Docker qualification separately verified,
on task-owned containers/volumes/networks only, a real Java 1.20.4 world
(`level.dat` plus real region files, EULA accepted), a real BlueMap v5.29 render
that produced actual map tiles, `/settings.json` reporting the target map
identity, a live pinned-image install and a legacy `:latest` adoption with
exact labels, owned-network IP, exposed port, read-only level bind and
unprivileged config. All of those fixtures were disposed after capture; no live
WSL module, server, world, network or host port was touched.

An owned-Chromium run drove the authenticated same-origin map path with those
real rendered assets and exercised the in-view install, idempotency and legacy
adoption journeys (4/4 scenarios, zero page/console errors). That run exposed a
real wiring defect: the component cast the grouped `rpcClient` to the flat
install seam, leaving `listModules`/`createModule` undefined at runtime. The
fix is an explicit adapter (`adaptBlueMapRpc`) with regression coverage.

A packaged, opt-in controller integration
(`internal/rpc/worldview_live_integration_test.go`, tag `integration_live`)
closes the originally reported discovery path against the REAL Go code: it
uses the real store/auth/RBAC components, a real Docker client pool against the
task-owned relay, real `bluemapdiscovery.Discovery` metadata inspection and the
REAL `worldview.NewProxyHandler` mounted on the controller mux. With no
explicit endpoint it found the installed module, verified all runtime gates,
and fetched `/settings.json` (target map identity) and a real rendered PNG tile
through the real proxy under a real granted token; wrong-scope and missing
tokens were denied before any upstream asset. That integration is NOT a live
controller-to-renderer install: the container/volumes/network were disposable
task-owned fixtures and the create/start journal was not driven.

Remaining limits: the browser run mocked ConnectRPC responses and used a local
same-origin proxy fixture, so it is UI evidence against real map assets, not
controller-to-renderer evidence. No cross-controller/distributed guarantee,
measured latency or production deployment is claimed. Do not read the unit
fixtures as live evidence.
