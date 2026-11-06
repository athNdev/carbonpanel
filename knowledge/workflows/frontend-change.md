---
type: Workflow
title: Frontend change
description: How to change the SvelteKit console and verify it without breaking the embedded build.
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
- A missing metric is unavailable, not zero. Render `—`, `IDLE` or `AWAITING
  SAMPLE`; never substitute a plausible-looking number.
- Use the approved artwork in [brand assets](../../assets/branding/README.md)
  for product-owned marks; preserve third-party and server-provided icons.
- Run `python scripts/check_brand_assets.py` from the repository root to check
  deployable logo copies and raster dimensions after a brand update.
- A surface that is not meant to scroll must not show a scrollbar, and a
  vertical scroller must reserve its gutter. A scrollbar that appears and
  disappears changes the content box, which can reflow the content and pull it
  back out: the rapid flicker this rule exists to stop. Tab strips use
  `scroll-strip`; vertical scrollers get `scrollbar-gutter: stable` from the
  `overflow-y-*` rule in `app.css`. `scrollbar-policy.test.ts` and
  `CarbonTabs.test.ts` fail if the policy is removed.
- Loading, empty and error states are part of the change, not follow-up work.
- A frontend-only fix must not bypass an authorization check in the backend.
- No page may scroll horizontally at 360/768/1440 widths. Wide tables and tab
  strips scroll inside their own `overflow-x-auto`/`scroll-strip` container;
  grid children get `min-w-0` (or `[&>*]:min-w-0` on the grid) so tracks cannot
  blow out; UUIDs and other unbreakable strings are shortened or `break-all`.
  Verify with the overflow audit pattern before calling a surface responsive.

# Read next

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
`tokens.test.ts`.
- Tertiary buttons are Carbon-quiet (accent text, transparent border, hover
  layer); the heavy foreground border is gone. Mono eyebrow labels use the
  `.mono-label` utilities in `app.css`, not scattered tracking utilities.
- Metric tiles share one hierarchy: `p-6`, sans semibold uppercase label,
  dividers only above footers, hover border. Stored display preferences
  (theme/motion/density) apply in `CarbonShell` on boot; see
  `display-preferences.test.ts`.

See [ADR 0009](../../docs/adr/0009-shared-design-system.md).
