---
type: Workflow
title: "Frontend change — Rules (part 2)"
description: "removed. Login, signup, route-change layout and sidebar collapse inherit that"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: frontend-change.md
---
  removed. Login, signup, route-change layout and sidebar collapse inherit that
  gutter; `layout-motion-policy.test.ts` fails if those surfaces opt out. Route
  view-transitions use `--motion-page` (140 ms), skip an in-flight transition
  when the next navigation starts, and are skipped under `prefersReducedMotion()`
  (`prefers-reduced-motion` and `[data-carbon-motion='reduced']`). Svelte
  `transition:` helpers must use `motionDuration()` so they snap under reduced
  motion. Animate color/opacity/transform only; do not `transition: all`. Cloud
  visual verification of the same states is out of this repository.
- Loading, empty and error states are part of the change, not follow-up work.
  Interactive components add DOM tests through their public props/events, and
  whole-page keyboard or reflow behavior is exercised by the owned-Chromium
  journey in `ci.yml`. Both use real components; see
  [Frontend accessibility coverage](../decisions/frontend-a11y-coverage.md).
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
- The WebSocket client owns callbacks and timers by connection generation.
  A replaced connection cannot update state or close its successor; explicit
  disconnect invalidates queued retries. Preserve subscription tail options
  when an authenticated connection returns. Exercise fake late events and the
  Chromium reconnect smoke in `scripts/manual-review/e2e/websocket-reconnect-check.mjs`.
- No page may scroll horizontally at 360/768/1440 widths. Wide tables and tab
  strips scroll inside their own `overflow-x-auto`/`scroll-strip` container;
  grid children get `min-w-0` (or `[&>*]:min-w-0` on the grid) so tracks cannot
  blow out; UUIDs and other unbreakable strings are shortened or `break-all`.
  Verify with the overflow audit pattern before calling a surface responsive.
- A pane whose header, toolbar and status line crowd a phone-height panel may
  raise the tab strip's floor (`max-sm:min-h`) and let the page scroll. The
  mods tab does this at both sizes so a preview reason stays visible at 390 px;
  its apply bar compacts and its policy editor opens in `CarbonModal`.
- Give each `CarbonTabs` item a reciprocal panel ID. Use
  `CarbonInactiveTabPanels` for empty, hidden targets so lazy content mounts
  only when selected. Make the active panel the next keyboard Tab stop.
  Packaged Chromium covers server detail and Modpacks.
