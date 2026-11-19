---
type: Decision
title: Frontend accessibility coverage
description: Component accessibility is tested in a real DOM and a real Chromium journey, not by matching source text.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/test/dom-setup.mjs
  - web/carbon-panel/test/a11y-fixture
  - web/carbon-panel/src/lib/components/carbon/a11y-dom.test.ts
  - scripts/manual-review/e2e/a11y-check.mjs
  - .github/workflows/ci.yml
sources:
  - title: Review browser driver
    resource: review-browser-driver.md
---

# Choice

DOM component tests mount the real Svelte components with Happy DOM globals and
a test-only Bun plugin that compiles `.svelte` with the client generator; the
test script passes `--conditions=browser` so Svelte resolves its client build.
A separate fixture mounts the same components with the real stylesheet and is
driven by the existing owned Chromium review harness in a CI job.

# Alternatives

Source-text assertions (the legacy `a11y-audit.test.ts` style) only prove a
string exists, not that focus moves, Escape closes, or a control has a role.
A new end-to-end framework or browser install mechanism would duplicate the
locked Playwright package and owned cache the review harness already provides.

# Consequence

Accessibility claims must name the engine actually exercised: Chromium only.
Firefox, WebKit and mobile-device emulation are not covered by this gate. New
interactive components should extend the DOM tests for public-seam behavior and
the journey only for whole-page keyboard/reflow properties.
