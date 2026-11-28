# Changelog

All notable changes to Carbon Panel are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- Paper, Purpur, Pufferfish, Folia, Spigot and Bukkit servers can browse and install plugins from Hangar (merged with Modrinth plugin results), with versions filtered by the server's Minecraft version and downloads written to the server's `plugins/` directory. Uploaded plugin jars are classified by their `plugin.yml` / `paper-plugin.yml` metadata.
- Server mods can check installed files against Modrinth by SHA-512 and preview a compatible release with its exact candidate file and checksum. The preview is read-only and does not change the server.
- Changing a server's Minecraft version is refused up front with the exact list of installed mods or plugins whose declared constraints cannot satisfy the target, instead of recreating into a broken state.
- Starting a server while its previous stop is still finishing waits for the stop instead of failing, and the interactive stop window is shorter, so rapid start and stop clicks can no longer hold the server in a long transition.
- The server map renders the Nether and the End alongside the Overworld, understands current save formats (including LZ4-compressed regions), and refreshes while Minecraft saves. A dimension picker keeps the camera in place, and the viewport states how old the visible save is.
- Unsaved edits across the server view collect into one floating bar that names the changed sections and saves or undoes them together. Start, Restart and Force Recreate are refused with a shake and a toast until those edits are saved.
- Mods and plugins share one browsing experience: the same search bar, result card and install-outcome list in the server view, the create-server add-ons step and the modpack studio, with dependency planning, per-row progress and a confirmed batch remove.

### Changed

- The mods browser shows what was added during the session: a count chip in the header, a collapsible "Added this session" summary, clearer installed badges and an explicit selected-version indicator.
- Loading indicators use a theme-consistent Carbon arc spinner everywhere, including buttons, inline states, the app boot screen and the server view. Page-level section grids fade in without vertical movement, and the boot screen reserves the app header's height so the swap does not shift layout.
- The server view's first-load state pairs the spinner with a skeleton of the runtime tiles, and every tab shares one loading treatment. The Engine Specs card fills its rows with unconditional Java, loader and modpack details.
- A frozen (paused) or deep-sleeping server explains itself when commands are refused, the metrics collector no longer queues executions against paused containers, RCON timeouts close their connections, and the reconciler converges a server's stored status as soon as its lifecycle transition ends.
- Crash loops surface as an errored status ("restarting repeatedly") instead of showing "starting" forever, container-event drops are counted and reported, and boot-failure detection recognises OOM and watchdog crash patterns.
- Copy is consistent across the console: one sentence case for buttons and titles, a single ellipsis character, "server" instead of "instance", "Minecraft & loader" instead of "Engine & Loader", "sign in" instead of "log in", and toasts without exclamation marks or a redundant "successfully".

### Fixed

- Container creation no longer corrupts daemon-side bind paths on deployments where the panel's filesystem differs from the daemon's (for example a Windows panel against a WSL2 or remote Linux daemon): the work-temp mount keeps the daemon path's separator style, and the data directory is pre-created from the panel-side path.
- Pressing Enter in the datapack search during server creation searches; it no longer submits the wizard.
- Players added manually to a server no longer report a fabricated "last seen"; identities without a session record show "Never observed" and sort after observed players.
- Player actions on the cross-server roster resolve each player's server from its allowlist record when no session exists, refuse empty targets with a clear message, and disable actions for rows without a server.
- Player heads resolve through the server's own `usercache.json` when Mojang cannot identify an offline-mode identity, and the player detail view shows the full skin texture.
- The map's "Copy map link" and the legacy standalone World View tab are removed; the map lives in the server's World tab and the library links there.
- Switching between server tabs no longer discards edits typed in the Overview or Configuration tab.
- The sign-in page no longer shows a replacement character where an ellipsis belongs, and the detach and delete dialogs describe the same outcome on both the server list and the server detail page.
- Nether maps render the terrain below the bedrock roof instead of a blank tile that claimed to be complete, and toggling the map's live updates no longer reloads every visible tile.

## [2.1.0] - 2026-11-21

### Added

- World View opens saved Overworld terrain for supported local Java servers. Pan and zoom with a pointer or keyboard, jump to spawn or coordinates, share a coordinate link, and view saved markers, waypoints and points of interest.
- Player lookup shows the account's name, UUID and full Minecraft skin when available. Server history follows the account's existing presence permissions.
- Create Server can queue compatible mods and module templates alongside existing modpack presets and datapacks. Review add-on results before first start and retry failed items on the server that was already created.
- World Library lists the current worlds of running servers you can access, and distinguishes managed revisions from server-local worlds.

### Changed

- The server's map preview shows surface materials and water depth. Open the map to see when Minecraft last saved the visible terrain. A ready BlueMap installation for the active world opens in the same view.
- Search server configuration by setting or environment variable, filter by category or unsaved edits, and expand individual settings for defaults and help. The same toolbar is used in Server Defaults.
- Server metrics group labels with their readings. The runtime card animates status changes and form fields use consistent hover, keyboard focus and error states in both themes.
- Notifications stack in the bottom-right corner and use the panel's theme, status icons and a keyboard-accessible dismiss button.
- World View offers adjustable coverage and renders saved terrain from qualifying partially generated chunks. World View rows link to the map from the full row.
- Modpacks uses a compact catalogue table with a details dialog for compatibility, runtime requirements, and indexed files. The server Players tab keeps the roster visible and opens allowlist entry in a dialog.

### Fixed

- World View opens from the server's World tab and recovers when a map session expires. Restoring a world invalidates old terrain sessions. Managed BlueMap works with registered local Docker nodes, and interrupted startup can reuse its container on retry.
- Recover BlueMap from retained configuration without rewriting it. The built-in map checks empty and incomplete visible tiles again after Minecraft has time to save.
- Responsive settings keep their Save controls visible, tabs have named panels and predictable keyboard focus, and modpack filters have accessible names.
- Dashboard totals distinguish measured zeroes, configured limits and unavailable or stale observations. The sidebar no longer displays a hard-coded product version.
- Theme selections apply immediately, persist across reloads and can follow the system setting. The dashboard scrolls past the System Health card, shows multiple Docker nodes from the sidebar, and gives server names and resource meters more room.
- Stable and Nightly documentation show their own changelogs and preserve the current page when switching channels.

## [2.0.1] - 2026-11-20

- The Carbon Panel 2.x release adds the World Library, player management and World View. Review the [2.0.1 release notes](https://github.com/athNdev/carbonpanel/releases/tag/carbonpanel-v2.0.1) before upgrading from 1.0.40.

## [3.0.0-alpha.1] - 2026-11-13

### Added

- Versioned release bundles for Linux amd64/arm64, macOS Intel/Apple Silicon and Windows amd64, with the embedded UI, storage helper, example configuration, licenses, build metadata and SHA-256 checksums. Both binaries report their release version with `--version`.

### Changed

- Versioned containers are mirrored to the repository-linked GitHub package. Skipped PR checks cannot interrupt CD, and dependency transport failures retry before tests run.
- Release publication validates exact-commit main CI and the complete native bundle/image set before publishing a GitHub release. Tagged images use the tested Linux downloads; main development images use `edge`, while stable releases own `latest`.
- Controller image builds compile the web UI natively and cross-compile pure-Go binaries, avoiding compiler work under ARM emulation.
- Design-system page documents all nineteen exported Carbon components with prop tables and behaviour notes, full light and dark token tables, and composition rules. A regression test now asserts every `carbon/index.ts` export has a documented section.
- Design-system page layout redo: every component pairs a captioned demo pane with a scrollable prop table, badges are fixed pills, and demo groups follow one spacing rhythm at 360, 768 and 1440 px.
- Server detail working panes (console, mods, datapacks, modules, files, tasks, backups, players, activity) fill the remaining column through a `min-h-0` / `flex-1` chain instead of a viewport-minus-chrome height, so the log, list, or tree scrolls inside the pane.
- Every server-detail tab shares one `ServerTabPanel` header (title, optional description and chip, right-aligned actions) and one filling container. Lists and logs scroll inside; forms keep their natural height.

## [3.0.0-beta.4] - 2026-11-07

### Added

- Global `+error.svelte` boundary with retry/home affordances.
- Server list now surfaces fetch failures with a retry action.
- ConnectRPC `buf breaking` CI job.
- Per-route error/empty/loading states (in progress).
- Reusable `CarbonErrorState` and `CarbonEmptyState` components exported from `carbon/index.ts`.
- Retryable error states for dashboard, ActiveModules, profile (API tokens), and settings (global settings).
- Docker daemon reachability/config integration tests behind the `integration` build tag (`internal/docker/integration_test.go`).
- Modpack export options (include-client/optional/overrides/summary), Prism/MultiMC instance export, and single Export dialog with data-loss preview.
- Dashboard node inventory cards and tile typography pass.
- Console fullscreen viewer, log search, and delete confirmation.
- Back up now: one-click native backups from the Backups tab (new CreateBackup RPC).
- Profile and Preferences popups; stop from deep sleep.
- Inter + JetBrains Mono webfonts (vendored, OFL).
- Showcase screenshot flow (devstack + journey plan + CD workflow).
- Landing product tour: borderless 2x feature crops with side copy (journey clips viewport shots to testid anchors).
- Studio list Export dropdown covering every launcher format; Stop buttons for deep-sleep/frozen servers on dashboard and server list.
- Tab indicator paints above active-tab backgrounds; tertiary buttons follow the Carbon spec (accent text, borderless); mono eyebrow labels share one tracking/color contract.
- Preferences Done closes the dialog; reduced-motion and compact-density choices take effect and apply on boot.
- Files tab: aligned columns, shift-free selection, Carbon-square context menu, filter-aware empty text; Docker Overrides single disclosure; server tiles match dashboard tile hierarchy.
- Showcase screenshots capture each page's full content column, keeping the homepage product tour large and uniform (4:3-16:10) instead of thin strips.
- Design-system guide: selection and context-menu state contract (hover, right-click, focus, Escape).
- Modpack Studio tabs for datapacks, resource packs and shader packs: Modrinth search, direct URL, archive upload, every packwiz metafile option (side, pin, optional/default/description), update checks, and a configurable datapack folder.
- Baking a pack now installs datapacks into the world datapacks folder (honouring `level-name`) and reports client-only content instead of dropping it silently.
- Open Graph and Twitter previews on every website page, with dedicated 1200x630 cards for the documentation home, introduction, Docker Compose, API reference and design system.
- Documentation: guides for Modpack Studio, server datapacks, staged updates, sleep and wake, and proxy routing; refreshed introduction, FAQ, troubleshooting and contributing pages.

### Fixed

- Social preview cards: balanced vertical spacing with a footer rule and a clearer URL line.
- Creating a server from a modpack keeps the RAM value you enter; the pack recommendation is shown as a hint instead of silently replacing it.
- Landing product tour: even rows now give the screenshot the wide column, so all four captures render at the same readable size.
- CurseForge export blocks with the full item list instead of silently dropping datapacks, resource packs, shader packs or mods it cannot carry.
- Fullscreen server console fills the viewport, keeps the toolbar and command bar, and no longer clips under the app header/sidebar. CarbonModal now portals dialogs to `<body>` so fixed overlays paint above the shell.
- Surface unchecked packwiz import/export and proxy route-refresh write errors.
- Make upload tests OS-independent (Windows temp-file locks, huge allocations).
- Accessibility gaps (aria-invalid/describedby, modal focus trap, table scope, reduced motion, toast aria-live).
- RBAC no longer denies every RPC for Clerk sessions whose org is not yet linked: falls back to the bootstrap default organization and auto-provisions membership.
- UNHEALTHY renders degraded yellow; boot healthcheck noise no longer flips starting servers.
- Game-process crashes detected from boot log and recorded as error with reason.
- Mod operations (install/upload/toggle/delete/stack) logged in Activity tab.
- Modpack servers name the real game loader instead of the platform.
- Tab indicator/confirm dialogs layering (indicator above active tab; modals teleport to body).
- Module table overflows; CarbonButton mapping sweep.
- Dependency vuln bumps (brotli, compress, x/crypto).
- Responsive overflow pass: wrapping page headers, grid track minimums, shortened node tags, breaking config values; modpack grids go single-column below lg.
- Trimmed the Docker auto-detect dialog copy.
- Light theme selection works again: the dark default no longer outranks an explicit light theme.
- Design-system homepage refresh: current Inter/JetBrains Mono type, tertiary/ghost/tab/tile demos, documented kind, label, tile and preference contracts, with regression tests.
- Files tab interactions: right-click selects the row under the cursor, selected rows keep a distinct hover state, clicking a row focuses the tree so arrow keys work immediately (with scroll-into-view), and Escape closes the context menu.

## [3.0.0-beta.3] - 2026-10-21

### Added

- Deep-sleep / wake-on-connect handling for Minecraft servers.
- Scheduler backup pruning and snapshot rollback safety checks.
- Module template and modpack studio improvements.

### Changed

- Proxy routing and node capacity handling hardened for remote nodes.

## [2.0.15] - 2026-09-30

### Added

- Multi-node clustering and remote Docker management.
- OIDC authentication providers.
- Modpack import/export and CurseForge/Modrinth indexers.

## [1.0.38] - 2026-09-15

### Added

- Initial single-node Minecraft server management.
- Web UI with Carbon Design System.
- Docker-based server isolation.

[Unreleased]: https://github.com/athNdev/carbonpanel/compare/carbonpanel-v2.1.0...HEAD
[2.1.0]: https://github.com/athNdev/carbonpanel/compare/carbonpanel-v2.0.1...carbonpanel-v2.1.0
[2.0.1]: https://github.com/athNdev/carbonpanel/compare/v1.0.40...carbonpanel-v2.0.1
[1.0.40]: https://github.com/athNdev/carbonpanel/releases/tag/v1.0.40
[3.0.0-alpha.1]: https://github.com/athNdev/carbonpanel/commit/5bbc06fefe5fc0c600672ef8401f9bf7e4495a01
[3.0.0-beta.4]: https://github.com/athNdev/carbonpanel/commit/896ea7a1859c7e96e9f496d3964bcabe100a1049
[3.0.0-beta.3]: https://github.com/discohaus/discopanel/releases/tag/v3.0.0-beta.3
[2.0.15]: https://github.com/discohaus/discopanel/releases/tag/v2.0.15
[1.0.38]: https://github.com/athNdev/carbonpanel/commit/fed0ca10b6ca2c78645f3003b6bbe75a313ffd2b
