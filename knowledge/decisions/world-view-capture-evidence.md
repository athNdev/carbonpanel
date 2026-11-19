---
type: Decision
title: "World View capture evidence"
description: "Fail-closed rules for proving rendered BlueMap terrain in the README World View capture."
status: stable
owners: [oss-runtime]
code_refs:
  - scripts/review/devstack.sh
  - scripts/review/journey.mjs
  - scripts/review/plan-showcase.json
  - scripts/review/fixtures/bluemap-webapp-jit
  - scripts/showcase_assets.py
  - scripts/showcase_assets.test.py
sources:
  - title: Parent concept
    resource: product-readme.md
  - title: Asset routing and CSP decision
    resource: ../../docs/adr/0048-bluemap-asset-routing-and-csp.md
---

The gated World View capture must prove *rendered* terrain, not just a mounted
iframe, a visible canvas and clean HTTP responses. Real BlueMap v5.29 tiles and
`settings.json` must come from a disposable seeded renderer; no mockup or
placeholder is publishable. The panel-wide CSP stays unchanged: the disposable
stack overlays the pinned, digest-checked CSP-safe webapp build (ADR 0048) over
the generated web root without touching `settings.json` or the real `maps/`
tile tree, and a digest drift fails the fixture closed.

The journey fails closed on renderer console errors (`unsafe-eval`,
`new Function`, `compileToFunction`, map load failure), and the asset gate
samples the map region separately from non-black panel chrome: a near-black
renderer cannot pass on surrounding UI alone. A capture is accepted only when
the downloaded PNG visually shows populated terrain and the manifest, raw URL,
SHA256 and provenance verify.
