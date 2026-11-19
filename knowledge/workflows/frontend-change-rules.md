---
type: Workflow
title: "Frontend change — Rules"
description: "SvelteKit 3 rejects any /server/ directory under src as server-only, even"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: frontend-change.md
---
# Rules

- SvelteKit 3 rejects any `/server/` directory under `src` as server-only, even
  for browser components. Keep shared UI in `components/server-ui/`; see ADR 0042.
- Keep the Carbon geometry, typography and token usage consistent with the
  existing shell.
- Start every primary route with `CarbonPageHeader`; do not hand-roll a title,
  description and action row. `CarbonShell` owns the page gutter, so route roots
  must not add another `p-6`/`p-8`.
- Use `CarbonButton` for actions. Labels stay on one line and icon-only buttons
  inherit the requested size.
- Keep UI scaffolding only when a retained feature consumes it. Before deleting
  a primitive group, trace imports, dynamic registries, generator inputs and
  documented interfaces; do not remove the entire legacy `ui/` directory.
  Select, dialog, table, dropdown, tooltip and resizable primitives still serve
  live features. Preserve the intentionally unmounted staged-configuration view.
  The retained `bits-ui` dependency requires `@internationalized/date` as a peer,
  even when the application has no direct date-component imports. Remove only
  exclusive dependencies and regenerate the lockfile through Bun.
- Proto generation uses pinned remote plugins rather than frontend-installed Buf
  or protoc generator executables. Keep the frontend protobuf runtime dependency;
  validate its compatibility through the actual generated client checks.
- A missing metric is unavailable, not zero. Render `—`, `IDLE` or `AWAITING
  SAMPLE`; never substitute a plausible-looking number.
- Use the approved artwork in [brand assets](../../assets/branding/README.md)
  for product-owned marks; preserve third-party and server-provided icons.
- Run `python scripts/check_brand_assets.py` from the repository root to check
  deployable logo copies and raster dimensions after a brand update.
- Server-detail working panes (console, lists, file tree, tables) fill the
  remaining height of `CarbonShell`'s main column through a `min-h-0` /
  `flex-1` chain and `.tab-fill`. Do not size them with `100vh` minus a chrome
  constant: that leftover is a dead region whenever the chrome is shorter than
  the constant. Every server-detail tab uses `ServerTabPanel`: one header
  (title, optional one-line description, optional chip, right-aligned
  `CarbonButton` size `sm` actions, shared min-height/padding/divider) and one
  bordered surface. Lists, logs and tables scroll inside the body (`mode=fill`);
  forms on Overview, Configuration, and Routing keep their natural height
  (`mode=form`) and scroll inside the same surface when they overflow.
  `tab-fill.test.ts` holds the contract.
- A surface that is not meant to scroll must not show a scrollbar, and a
  vertical scroller must reserve its gutter. A scrollbar that appears and
  disappears changes the content box, which can reflow the content and pull it
  back out: the rapid flicker this rule exists to stop. Tab strips use
  `scroll-strip`; vertical scrollers get `scrollbar-gutter: stable` from the
  `overflow-y-*` rule in `app.css`. Nested `overflow-x-auto` panes that are not
  also vertical scrollers hide the bar (same height contract as `scroll-strip`)
  so a table overflowing after sidebar collapse cannot grow a horizontal bar and
  feed an ancestor gutter. Dual-axis panes (console, logs) keep a visible bar.
  `scrollbar-policy.test.ts` and `CarbonTabs.test.ts` fail if the policy is
