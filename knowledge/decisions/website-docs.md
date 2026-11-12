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
- The shared design system is published with the homepage from the current
  branch, including its canonical tokens. It shares site branding, local fonts
  and navigation with the website and docs, while keeping its component
  specimens and theme preview independent. It is not pinned application content.
- The website theme and docs navigation can evolve independently of the source
  pin. Display the pin on every documentation page.
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
- Current social cards are rendered from `assets/branding/social-preview.html`
  with the approved logo, homepage terrain and Plex fonts. The website uses
  1200 by 630 PNGs; GitHub has a separate 1280 by 640 repository PNG and an
  independent upload setting. Regeneration must not alter the approved logo.

# Rationale

Independent Pages deployments overwrite one another. A single artifact preserves
both routes, and an explicit source pin prevents examples and API schemas from
silently following unreleased changes.

# Verification

Use the commands and review procedure in the publishing guide. Snapshot-isolation
tests verify that working-tree edits cannot leak into pinned docs or examples.
