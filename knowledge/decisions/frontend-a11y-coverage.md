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
driven by the existing owned Chromium review harness in a CI job. The same
journey then drives real application routes from the SvelteKit production static
build (the assets the Go binary embeds), served by a loopback static server with
SPA fallback. ConnectRPC responses are seeded only at the network boundary with
`page.route`; no source module, store or component internal is imported or
driven. Representative routes cover shell focus order, dialog focus trap and
Escape, tabs, accessible names and roles, empty/populated/error states and
reflow at 360/768/1440.

# Alternatives

Source-text assertions (the legacy `a11y-audit.test.ts` style) only prove a
string exists, not that focus moves, Escape closes, or a control has a role.
A new end-to-end framework or browser install mechanism would duplicate the
locked Playwright package and owned cache the review harness already provides.

# Consequence

Accessibility claims must name the engine actually exercised: Chromium only.
Firefox, WebKit, mobile-device emulation, screen readers and 400% zoom are not
covered by this gate. The route checks exercise the packaged SPA with a seeded
API boundary, not an authenticated live controller, RPC service, Docker daemon
or Minecraft gameplay. They do not duplicate the screenshot review lane. New
interactive components should extend the DOM tests for public-seam behavior and
the journey for whole-page keyboard, role, state and reflow properties.
