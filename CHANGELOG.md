# Changelog

All notable changes to Carbon Panel are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

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

[Unreleased]: https://github.com/athNdev/carbon-panel/compare/v3.0.0-beta.4...HEAD
[3.0.0-beta.4]: https://github.com/athNdev/carbon-panel/releases/tag/v3.0.0-beta.4
[3.0.0-beta.3]: https://github.com/athNdev/carbon-panel/releases/tag/v3.0.0-beta.3
[2.0.15]: https://github.com/athNdev/carbon-panel/releases/tag/v2.0.15
[1.0.38]: https://github.com/athNdev/carbon-panel/releases/tag/v1.0.38
