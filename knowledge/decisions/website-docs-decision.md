---
type: Decision
title: "Website and documentation publication — Decision"
description: "The homepage and documentation share one artifact. Only the website deployment"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: website-docs.md
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
- Application prose, imported examples and API schema come from the same full commit in `docs/website-source.json`; never substitute the current checkout or a floating branch. Exception: release-independent operational guides declared in `docs/maintained-guides.json` derive their exact source revision from the trusted checkout at build time (never pinned in the manifest) and attest blob digests; product pins, aliases and schema stay untouched. See ADR 0050.
- `docs/website-source.json` is the single build contract; channel and
  availability are derived from the classified catalog
  `docs/release-history.json`, never from a `v` prefix. The generated
  `docs/version-index.json` is shared by the banner, version switcher, direct
  URL validation and Pages metadata. Every entry is validated against the
  catalog peel and the real Git tag; a disagreement fails closed before
  packaging. See ADR 0045.
- Stable docs default to `/docs/`; immutable per-version routes
  `/docs/versions/<display-version>/` are built from exact peeled commits for
  `1.0.40` (stable), `3.0.0-alpha.1` (GHCR-only legacy preview) and `1.0.39`
  (source-only, no download CTA). `v1.0.38` and the misbound `v3.0.0-beta.4`
  are excluded. The common switcher carries the current page when the target
  version has it, otherwise it lands on the target home.
- `scripts/version_bump.py prepare`/`bump` is the one atomic workflow; it
  derives or validates every surface on the same commit, checks rendered and
  packaged output, and never tags, publishes or marks a candidate published.
- The proto's `2.x` is the independent Connect-OpenAPI schema family, not a
  product version. Bundled and website OpenAPI copies stamp `info.version`
  with the exact normalized product version (matching CLI/UI/release manifest)
  and are verified fail-closed; the `carbonpanel.v1` protocol stays unchanged.
- The shared design system is published with the homepage from the current
  branch, including its canonical tokens. It shares site branding, local fonts
  and navigation with the website and docs, while keeping its component
  specimens and theme preview independent. It is not pinned application content.
- The website theme and docs navigation can evolve independently of the source
  pin. Display the pin on every documentation page.
- Homepage closing-banner columns divide the capped content frame. Do not
