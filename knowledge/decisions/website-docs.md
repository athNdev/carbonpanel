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
sources:
  - title: Website publishing
    resource: ../../docs/website-publishing.md
---

# Decision

- The homepage and documentation share one artifact. Only the website deployment
  workflow publishes Pages; docs validation must not replace the root homepage.
- Application prose, imported examples and API schema come from the same full
  commit in `docs/website-source.json`. Never substitute the current checkout or
  a floating branch if that commit is unavailable.
- The standalone shared design system is published with the homepage from the
  current branch, including its canonical tokens. It is not application content.
- The website theme and docs navigation can evolve independently of the source
  pin. Display the pin on every documentation page.
- A development commit must be labelled as a development snapshot. Do not infer
  a stable release from a package version or an inherited tag.
- Every published page declares the full Open Graph and Twitter set: type,
  site name, title, description, URL, a versioned absolute 1200 by 630 image
  and its alt text. The homepage, documentation home and any page without a
  dedicated card share `landing/assets/og.png`.
- Prominent routes (documentation home, introduction, Docker Compose, API
  reference, design system) use a dedicated card. `check_website.py` rejects a
  page that is missing metadata, points at a relative or missing image, or lets
  a prominent route fall back to the shared card. Keep preview copy free of em
  dashes.

# Rationale

Independent Pages deployments overwrite one another. A single artifact preserves
both routes, and an explicit source pin prevents examples and API schemas from
silently following unreleased changes.

# Verification

Use the commands and review procedure in the publishing guide. Snapshot-isolation
tests verify that working-tree edits cannot leak into pinned docs or examples.
