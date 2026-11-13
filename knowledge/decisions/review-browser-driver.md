---
type: Decision
title: Review browser driver
description: Both review profiles use the same locked driver and an owned browser cache.
status: stable
owners: [oss-runtime]
code_refs:
  - scripts/review/browser.mjs
  - scripts/review/setup-browser.mjs
  - scripts/review/browser-check.mjs
  - scripts/review/browser.test.mjs
  - scripts/review/run.mjs
  - scripts/review/report.mjs
  - scripts/review/profile.test.mjs
  - scripts/review/profile-browser.test.mjs
  - scripts/review/journey.mjs
  - scripts/manual-review/e2e/run.mjs
  - .github/workflows/review-ownership.yml
sources:
  - title: Review toolkit
    resource: ../../scripts/review/README.md
---

# Choice

Both browser review profiles load the exact Playwright version locked in the
existing e2e package. Setup requires Node 24 and installs the driver's Chromium
into the checkout's ignored, version-specific cache with browser garbage
collection disabled. It does not search home module caches or remove browsers
belonging to other projects. Explicit browser/cache overrides remain supported.

# Evidence

Each run records driver and actual browser versions, executable/cache paths and
override status in browser.json. These facts identify its UI evidence; they do
not establish lifecycle or gameplay acceptance. Native Windows and Linux CI
exercise a real browser launch with the shared driver.

The shared entrypoint selects single-seeded or dual-ownership explicitly.
Both profiles emit schema-versioned review.json with the same UI-review scope,
driver metadata and check outcomes. Empty evidence is incomplete; findings
requiring human review remain distinct from passed checks. Critical setup or
login errors stop navigation, return failure and close the browser. The
machine report removes credentials, query parameters and fragments from its
base URL; detailed screenshots and reports remain local review artifacts.
