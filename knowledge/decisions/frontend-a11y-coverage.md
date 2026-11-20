---
type: Decision
title: Frontend accessibility coverage
description: Component and real application-route accessibility are tested in a real DOM and a real Chromium journey, not by matching source text.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/test/dom-setup.mjs
  - web/carbon-panel/test/a11y-fixture
  - web/carbon-panel/test/a11y-routes/serve.mjs
  - web/carbon-panel/src/lib/components/carbon/a11y-dom.test.ts
  - scripts/manual-review/e2e/a11y-check.mjs
  - scripts/review/plan-modpacks-library.json
  - scripts/review/plan-modpacks-studio-library.json
  - .github/workflows/ci.yml
  - .github/workflows/modpacks-review.yml
sources:
  - title: Review browser driver
    resource: review-browser-driver.md
---

# Choice

DOM component tests mount the real Svelte components with Happy DOM globals and
a test-only Bun plugin that compiles `.svelte` with the client generator; the
test script passes `--conditions=browser` so Svelte resolves its client build.
A separate fixture mounts the same components with the real stylesheet and is
driven by the existing owned Chromium review harness in a CI job. The same
journey then drives real application routes from the SvelteKit production static
build (the assets the Go binary embeds), served by a loopback static server with
SPA fallback. ConnectRPC responses are seeded only at the network boundary with
`page.route`; no source module, store or component internal is imported or
driven. Representative routes cover shell focus order, dialog focus trap and
Escape, tabs, accessible names and roles, empty/populated/error states and
reflow at 360/768/1440. The high-value product routes — dashboard, players,
world library and world view — are additionally checked in both the light and
dark Carbon themes at each width. Theme is selected through the persisted
`carbon-theme` preference, so the assertions observe the real
`data-carbon-theme` attribute and painted background rather than toggling a
class from the test. The shell skip link and profile controls restore keyboard focus to the owning trigger. Dashboard and inventory fleet tables keep the horizontal scroll region on the labelled wrapper at every width. Long KPI values and status footers wrap when the expanded navigation reduces available space.

# Alternatives

Source-text assertions (the legacy `a11y-audit.test.ts` style) only prove a
string exists, not that focus moves, Escape closes, or a control has a role.
A new end-to-end framework or browser install mechanism would duplicate the
locked Playwright package and owned cache the review harness already provides.

# Consequence

Chromium is the primary gate. Firefox and WebKit run smoke checks. Screen
readers, 400% zoom and mobile-device emulation are outside the coverage. Seeded
SPA checks exercise the frontend boundary, including refused `SearchModpacks`
and Retry. Extend DOM and page journey tests for new interactive controls.

On relevant PRs and main changes, the Modpacks browser review runs both plans
against an owned authenticated controller. It checks the Modrinth-backed
catalog, unmatched search and recovery, and the Studio library entry controls.
Product journeys and screenshot publication have separate evidence.
