---
type: Workflow
title: Frontend change
description: How to change the SvelteKit console, ServerTabPanel tab-fill chrome, and verify it without breaking the embedded build.
status: stable
owners: [oss-runtime]
code_refs: [web/carbon-panel, scripts/web-build.sh]
sources:
  - title: SvelteKit resolve navigation ADR
    resource: ../../docs/adr/0002-sveltekit-resolve-navigation.md
  - title: SvelteKit 3 security migration ADR
    resource: ../../docs/adr/0042-sveltekit3-security-migration.md
---

# Parts

- [Frontend change — Steps](frontend-change-steps.md) — Work in web/carbon-panel (SvelteKit 3, Svelte 5, IBM Carbon tokens).
- [Frontend change — Rules](frontend-change-rules.md) — SvelteKit 3 rejects any /server/ directory under src as server-only, even
- [Frontend change — Rules (part 2)](frontend-change-rules-2.md) — removed. Login, signup, route-change layout and sidebar collapse inherit that
- [Frontend change — Read next + Shared design system](frontend-change-read-next.md) — Shared homepage feature components also run in the fixture-only build. After

# Key facts

- Server-detail working panes (console, lists, file tree, tables) fill the
- the constant. Every server-detail tab uses `ServerTabPanel`: one header
- `tab-fill.test.ts` holds the contract.
- `flex-1` chain and `.tab-fill`. Do not size them with `100vh` minus a chrome
- back out: the rapid flicker this rule exists to stop. Tab strips use
- gutter; `layout-motion-policy.test.ts` fails if those surfaces opt out. Route
- (`prefers-reduced-motion` and `[data-carbon-motion='reduced']`). Svelte
- `bare` modes let a surface own the whole viewport. The server console's
- SvelteKit 3 rejects any `/server/` directory under `src` as server-only, even
- for browser components. Keep shared UI in `components/server-ui/`; see ADR 0042.
- Proto generation uses pinned remote plugins rather than frontend-installed Buf
- or protoc generator executables. Keep the frontend protobuf runtime dependency;
- for product-owned marks; preserve third-party and server-provided icons.
- bordered surface. Lists, logs and tables scroll inside the body (`mode=fill`);
- A surface that is not meant to scroll must not show a scrollbar, and a
- vertical scroller must reserve its gutter. A scrollbar that appears and
- `scroll-strip`; vertical scrollers get `scrollbar-gutter: stable` from the
- also vertical scrollers hide the bar (same height contract as `scroll-strip`)
- feed an ancestor gutter. Dual-axis panes (console, logs) keep a visible bar.
- `scrollbar-policy.test.ts` and `CarbonTabs.test.ts` fail if the policy is
- removed. Login, signup, route-change layout and sidebar collapse inherit that
- view-transitions use `--motion-page` (140 ms), skip an in-flight transition
- `transition:` helpers must use `motionDuration()` so they snap under reduced
- motion. Animate color/opacity/transform only; do not `transition: all`. Cloud
- [Frontend accessibility coverage](../decisions/frontend-a11y-coverage.md).
- `FileEditorSession` identity (generation, server ID and path). Closing or
- UI callbacks also verify the currently displayed props before closing or
- Carbon controls and Monaco with deferred RPC responses to verify this seam.
- fullscreen mode renders the same pane as the inline console, so the toolbar,
- A frontend-only fix must not bypass an authorization check in the backend.
- The Docker Overrides JSON editor normalizes parsed JSON to a `DockerOverrides`
  message; a plain object breaks save snapshots and leaves the form dirty.
- No page may scroll horizontally at 360/768/1440 widths. Wide tables and tab
- Verify with the overflow audit pattern before calling a surface responsive.
- [RPC contract](../architecture/rpc-contract.md)
- (theme/motion/density) are owned by `lib/display-preferences.svelte.ts`:
