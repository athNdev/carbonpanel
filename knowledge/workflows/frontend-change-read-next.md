---
type: Workflow
title: "Frontend change — Read next + Shared design system"
description: "Shared homepage feature components also run in the fixture-only build. After"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: frontend-change.md
---
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
- Panel defaults to System, resolving live OS changes to Light or Dark. The
  root layout hydrates the preference owner on every route. It synchronizes the
  dark class with the resolved `data-carbon-theme` for tokens, editors and toasts.
  Explicit choices persist under `carbon-theme`. The token fallback
  `:where(:root)` remains dark before preferences apply.
- Tertiary buttons are Carbon-quiet (accent text, transparent border, hover
  layer); the heavy foreground border is gone. Mono eyebrow labels use the
  `.mono-label` utilities in `app.css`, not scattered tracking utilities.
- Metric tiles share one hierarchy: `p-6`, sans semibold uppercase label,
  dividers only above footers, hover border. Stored display preferences
  (theme, motion, density and server-detail metric disclosure) are owned by `lib/display-preferences.svelte.ts`:
  it validates, persists and applies `data-carbon-*` attributes. The preferences dialog and toast host consume its stores, and code
  editors take their initial theme from it and follow live changes through an
  already-loaded runtime (never bootstrapping Monaco for a preference change).
  See `display-preferences.test.ts`. Per-feature token migrations extend the
  new-debt ratchet in that test one surface at a time; a listed file must have
  zero six-digit hex. Purposeful status colors become semantic tokens
  (`--destructive-text`, `--success-text`, `--accent-text`), not raw hex. The
  ratchet is not a whole-panel claim, and Cloud's content-pinned token snapshot
  is not edited by a Panel token migration.

Native text-entry controls and select triggers share the panel field rule in `app.css` and the theme's `--field-*` tokens. Invalid fields keep their error edge on focus. Read-only fields have a dashed edge.

Server Configuration and global Server Defaults share the category/search editor. Filtering preserves edits across categories. Copied setting links include the owning tab and property-key fragment so a fresh visit reveals the field.

The root Sonner wrapper gives notifications one bottom-right stack with
theme tokens, status icons, safe-area insets, and a keyboard-reachable dismiss control.

See [ADR 0009](../../docs/adr/0009-shared-design-system.md).
