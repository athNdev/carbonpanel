---
type: Decision
title: "Website and documentation publication — Decision (part 2)"
description: "reserve artwork space with viewport-relative percentage padding on that"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: website-docs.md
---
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
