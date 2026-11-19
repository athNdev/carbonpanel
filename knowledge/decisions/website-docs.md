---
type: Decision
title: Website and documentation publication
description: Publish the homepage and commit-pinned application documentation as one Pages artifact with independent presentation.
status: stable
owners: [oss-runtime]
code_refs: [.github/workflows/landing.yml, .github/workflows/docs.yml, docs/website-source.json, docs/version-index.json, docs/adr/0045-version-contract-and-multi-version-docs.md, scripts/version_contract.py, scripts/version_bump.py, scripts/openapi_version.py, scripts/test_openapi_version.py, scripts/browser_version_check.py, scripts/test_version_contract.py, internal/rpc/handlers/openapi.go, docs/carbon-panel, docs/design-system, docs/carbon-panel/src/components/SocialHead.astro, landing/assets, web/carbon-panel/src/lib/design-system/tokens.css, scripts/build_website.py, scripts/check_website.py, scripts/generate_social_cards.py, web/carbon-panel/demo]
sources:
  - title: Website publishing
    resource: ../../docs/website-publishing.md
---

# Parts

- [Website and documentation publication — Decision](website-docs-decision.md) — The homepage and documentation share one artifact. Only the website deployment
- [Website and documentation publication — Decision (part 2)](website-docs-decision-2.md) — reserve artwork space with viewport-relative percentage padding on that
- [Website and documentation publication — Rationale + Verification](website-docs-rationale.md) — Independent Pages deployments overwrite one another. A single artifact preserves
