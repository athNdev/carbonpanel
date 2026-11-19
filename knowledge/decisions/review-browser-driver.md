---
type: Decision
title: Review browser driver
description: Both review profiles use the same locked driver and an owned browser cache.
status: stable
owners: [oss-runtime]
code_refs: [scripts/review/browser.mjs, scripts/review/setup-browser.mjs, scripts/review/browser-check.mjs, scripts/review/browser.test.mjs, scripts/review/run.mjs, scripts/review/report.mjs, scripts/review/profile.test.mjs, scripts/review/profile-browser.test.mjs, scripts/review/journey.mjs, scripts/review/entry-state.mjs, scripts/review/entry-state.test.mjs, scripts/manual-review/e2e/run.mjs, scripts/manual-review/e2e/a11y-check.mjs, .github/workflows/review-ownership.yml, .github/workflows/ci.yml]
sources:
  - title: Review toolkit
    resource: ../../scripts/review/README.md
---

# Parts

- [Review browser driver — Choice + Evidence](review-browser-driver-choice.md) — Both browser review profiles load the exact Playwright version locked in the
