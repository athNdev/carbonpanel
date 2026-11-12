---
type: Workflow
title: Frontend change
description: How to change the SvelteKit console, ServerTabPanel tab-fill chrome, and verify it without breaking the embedded build.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel
  - scripts/web-build.sh
sources:
  - title: SvelteKit resolve navigation ADR
    resource: ../../docs/adr/0002-sveltekit-resolve-navigation.md
---

# Steps

1. Work in `web/carbon-panel` (SvelteKit 2, Svelte 5, IBM Carbon tokens).
2. Use the generated Connect client from `src/lib/proto`; do not add a second
   API layer.
3. Run:

```sh
cd web/carbon-panel
bun install --frozen-lockfile
bun run check
bun run test
```

4. Build the embedded assets with `bash scripts/web-build.sh` before a
   production or binary build.

# Rules

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
  removed. Login, signup, route-change layout and sidebar collapse inherit that
  gutter; `layout-motion-policy.test.ts` fails if those surfaces opt out. Route
  view-transitions use `--motion-page` (140 ms), skip an in-flight transition
  when the next navigation starts, and are skipped under `prefersReducedMotion()`
  (`prefers-reduced-motion` and `[data-carbon-motion='reduced']`). Svelte
  `transition:` helpers must use `motionDuration()` so they snap under reduced
  motion. Animate color/opacity/transform only; do not `transition: all`. Cloud
  visual verification of the same states is out of this repository.
- Loading, empty and error states are part of the change, not follow-up work.
- Reused file-editor dialogs keep content and async requests under one
  `FileEditorSession` identity (generation, server ID and path). Closing or
  selecting another file invalidates earlier load/save completions, including
  their error and cleanup callbacks. A save commits its captured revision;
  newer edits remain dirty. Test delayed completions through the session's
  actual read/write adapters, including the same path on different servers.
  UI callbacks also verify the currently displayed props before closing or
  notifying: props can change before Svelte flushes the session effect. Keep
  state used by a pre-effect initialized before registering that effect.
  `scripts/manual-review/e2e/editor-dialog-check.mjs` mounts the real dialog,
  Carbon controls and Monaco with deferred RPC responses to verify this seam.
- Overlays that must sit above the app chrome portal to `<body>`. A fixed
  overlay rendered inside `<main>` is trapped by the page stacking context: the
  fixed header and sidebar paint over it and the scroll container clips it.
  `CarbonModal` portals every dialog for this reason; its `fullscreen` and
  `bare` modes let a surface own the whole viewport. The server console's
  fullscreen mode renders the same pane as the inline console, so the toolbar,
  command bar and tail controls never drop out.
- A frontend-only fix must not bypass an authorization check in the backend.
- No page may scroll horizontally at 360/768/1440 widths. Wide tables and tab
  strips scroll inside their own `overflow-x-auto`/`scroll-strip` container;
  grid children get `min-w-0` (or `[&>*]:min-w-0` on the grid) so tracks cannot
  blow out; UUIDs and other unbreakable strings are shortened or `break-all`.
  Verify with the overflow audit pattern before calling a surface responsive.

# Read next

Shared homepage feature components also run in the fixture-only build. After
changing `src/lib/components/fragments`, run `bun run build:homepage` and the
combined website checks. Keep network and authorization in the production
wrapper; see [Homepage UI fragments](../decisions/homepage-fragments.md).

- [RPC contract](../architecture/rpc-contract.md)
- [Verification](../testing/verification.md)

# Shared design system

Use [the shared guide](../../docs/design-system/README.md) and canonical CSS
tokens for new UI. Cloud carries a content-pinned snapshot; changes need review
and verification in both products. The standalone design-system homepage lives
in docs/design-system and is not a Panel application route. Literal colors in older views remain migration debt, so a token
change alone does not establish application-wide propagation.
- Dark is the default theme: `:root` carries the dark palette and light applies
only under `[data-carbon-theme='light']`. Controls that render white on a
fresh profile mean a token regressed to the light default; see
`tokens.test.ts`. The default selector is `:where(:root)` so it never
outranks an explicit theme; do not 'simplify' it back to `:root`.
- Tertiary buttons are Carbon-quiet (accent text, transparent border, hover
  layer); the heavy foreground border is gone. Mono eyebrow labels use the
  `.mono-label` utilities in `app.css`, not scattered tracking utilities.
- Metric tiles share one hierarchy: `p-6`, sans semibold uppercase label,
  dividers only above footers, hover border. Stored display preferences
  (theme/motion/density) apply in `CarbonShell` on boot; see
  `display-preferences.test.ts`.

See [ADR 0009](../../docs/adr/0009-shared-design-system.md).
