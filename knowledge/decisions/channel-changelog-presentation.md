---
type: Decision
title: Channel changelog is generated presentation, not pinned prose
description: The public changelog is rendered from the one version contract plus hash-attested GitHub release notes, with a separate channel-presentation fingerprint for docs-only publication.
status: stable
owners: [oss-runtime]
code_refs:
  - scripts/changelog_page.py
  - scripts/build_website.py
  - scripts/docs_publication_gate.py
  - scripts/test_website.py
  - docs/changelog-source.json
  - docs/release-notes/carbonpanel-v2.0.1.md
  - docs/carbon-panel/src/components/VersionBanner.astro
  - .github/workflows/docs-only-publish.yml
sources:
  - title: Website and documentation publication
    resource: ../../docs/website-publishing.md
  - title: Maintained operational guides (ADR 0050)
    resource: ../../docs/adr/0050-maintained-operational-guides.md
---

# Decision

- The pinned `changelog.md` was development-preview prose (second H1, stale
  1.0.40 note, inherited tag names that are not Carbon Panel releases). It is
  removed from each built snapshot and replaced by a generated `changelog.mdx`
  with exactly one H1 from frontmatter. Canonical and immutable archive routes
  receive the same channel-correct presentation.
- Identity, tag, revision, digest, channels and counterpart routes come only
  from the generated version contract. Every validated Stable tag gets its exact
  canonical GitHub release-notes link; a local body is included only when the
  source declaration attests its SHA-256, and upgrade guidance is linked only
  where that tag has a validated anchor. A future or tracked 1.0.40 Stable with
  no local body never aborts the website build.
- Nightly shows its exact qualified revision, digest and promotion state, a
  comparison to the verified Stable, and the Stable baseline notes without
  claiming a GitHub release or retired/colliding tag history.
- Changelog source, release notes, generator and `VersionBanner.astro` form a
  SEPARATE channel-presentation fingerprint. It is stamped into the built
  `presentation.json` and compared by the docs-only publication gate, so a
  switcher-only fix reconciles Pages. It is never folded into the maintained
  operational guide declaration or receipt (ADR 0050).

# Read next

- [Website and documentation publication](website-docs.md)
- [Maintained guide publication](maintained-guide-publication.md)
