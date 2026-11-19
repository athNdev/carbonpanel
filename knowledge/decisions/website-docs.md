---
type: Decision
title: Website and documentation publication
description: Publish the homepage and commit-pinned application documentation as one Pages artifact with independent presentation.
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows/landing.yml
  - .github/workflows/docs.yml
  - docs/website-source.json
  - docs/version-index.json
  - docs/adr/0043-version-contract-and-multi-version-docs.md
  - scripts/version_contract.py
  - scripts/version_bump.py
  - scripts/test_version_contract.py
  - docs/carbon-panel
  - docs/design-system
  - docs/carbon-panel/src/components/SocialHead.astro
  - landing/assets
  - web/carbon-panel/src/lib/design-system/tokens.css
  - scripts/build_website.py
  - scripts/check_website.py
  - scripts/generate_social_cards.py
  - web/carbon-panel/demo
sources:
  - title: Website publishing
    resource: ../../docs/website-publishing.md
---

# Decision

- The homepage and documentation share one artifact. Only the website deployment
  workflow publishes Pages; docs validation must not replace the root homepage.
- An ordinary main merge only builds and validates the combined site. Pages
  deployment requires `workflow_dispatch` with `deploy=true`, a read-only
  exact-commit main-CI verification and a trusted `refs/heads/main` ref. Build and indexing jobs stay read-only;
  `pages: write` and `id-token: write` belong only to the deploy job. IndexNow
  notification is deliberately deferred: changed URLs come from a push diff
  while deploy is dispatch-only, so no notification is sent. See
  [release operations](../workflows/release.md).
- Website CI pins the same Bun version as the frontend CI so frozen installs
  can read the application's checked-in lockfile format.
- The source-build guide installs frontend dependencies with the frozen lockfile
  before generation, which requires the pinned local formatter. It names the
  explicit container builder image overrides and version-policy record.
- The homepage's interactive examples compile from shared Svelte feature
  components with browser-local fixtures; the combined build includes their
  prerendered HTML and hashed assets. See [Homepage UI fragments](homepage-fragments.md).
- Application prose, imported examples and API schema come from the same full
  commit in `docs/website-source.json`. Never substitute the current checkout or
  a floating branch if that commit is unavailable.
- `docs/website-source.json` is the single build contract; channel and
  availability are derived from the classified catalog
  `docs/release-history.json`, never from a `v` prefix. The generated
  `docs/version-index.json` is shared by the banner, version switcher, direct
  URL validation and Pages metadata. Every entry is validated against the
  catalog peel and the real Git tag; a disagreement fails closed before
  packaging. See ADR 0043.
- Stable docs default to `/docs/`; immutable per-version routes
  `/docs/versions/<display-version>/` are built from exact peeled commits for
  `1.0.40` (stable), `3.0.0-alpha.1` (GHCR-only legacy preview) and `1.0.39`
  (source-only, no download CTA). `v1.0.38` and the misbound `v3.0.0-beta.4`
  are excluded. The common switcher carries the current page when the target
  version has it, otherwise it lands on the target home.
- `scripts/version_bump.py prepare`/`bump` is the one atomic workflow; it
  derives or validates every surface on the same commit, checks rendered and
  packaged output, and never tags, publishes or marks a candidate published.
- The shared design system is published with the homepage from the current
  branch, including its canonical tokens. It shares site branding, local fonts
  and navigation with the website and docs, while keeping its component
  specimens and theme preview independent. It is not pinned application content.
- The website theme and docs navigation can evolve independently of the source
  pin. Display the pin on every documentation page.
- Homepage closing-banner columns divide the capped content frame. Do not
  reserve artwork space with viewport-relative percentage padding on that
  frame: it collapses the copy at ultrawide sizes. The mobile layout uses one
  content column above the terrain.
- The craft section raises its local grid contrast without changing the other
  homepage grids, and preserves the fade around the content frame. Its square
  grid uses both axes at every viewport size, with smaller cells and a full-width
  fade on mobile; the general mobile grid suppression does not apply here.
- A published release pin uses the release tag as its label. Any other commit is
  labelled a development snapshot. Do not infer a release from a package version
  or an inherited tag; the label names an explicit tag or the maintainer's
  snapshot choice.
- Every published page declares the full Open Graph and Twitter set: type,
  site name, title, description, URL, a versioned absolute 1200 by 630 image
  and its alt text. The homepage, documentation home and any page without a
  dedicated card share `landing/assets/og.png`.
- Prominent routes (documentation home, introduction, Docker Compose, API
  reference, design system) use a dedicated card. `check_website.py` rejects a
  page that is missing metadata, points at a relative or missing image, or lets
  a prominent route fall back to the shared card. Keep preview copy free of em
  dashes.
- The combined Pages artifact generates its root `sitemap.xml` from every
  indexable published HTML route except the synthetic 404 page, with the
  deployment date as `lastmod`; utility pages with `noindex` are excluded.
  Website validation rejects missing or incomplete output.
- Current social cards are rendered from `assets/branding/social-preview.html`
  with the approved logo, homepage terrain and Plex fonts. The crop centers
  the lit doorway and nearby terraces so they read at thumbnail size. The website uses
  1200 by 630 PNGs; GitHub has a separate 1280 by 640 repository PNG and an
  independent upload setting. Regeneration must not alter the approved logo.

# Rationale

Independent Pages deployments overwrite one another. A single artifact preserves
both routes, and an explicit source pin prevents examples and API schemas from
silently following unreleased changes.

# Verification

Use the commands and review procedure in the publishing guide. Snapshot-isolation
tests verify that working-tree edits cannot leak into pinned docs or examples.
Serve `landing/qa-closing-layout.html` for browser checks of the actual closing
banner from 320px through 3440px, including desktop title and link wrapping.
