---
type: Decision
title: "Review browser driver — Choice + Evidence"
description: "Both browser review profiles load the exact Playwright version locked in the"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: review-browser-driver.md
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
Default artifacts are timestamped beneath the checkout's ignored tmp tree,
independent of the caller's working directory; explicit output overrides are
supported for attaching evidence elsewhere.

The login route hydrates after DOMContentLoaded, so bootstrap/login/auth/fatal
selection waits for one mutually exclusive semantic DOM state instead of an
immediate pre-hydration `count()`; no reachable state fails closed. A delayed-
hydration fixture covers the seeded-login regression. The frontend accessibility
journey reuses this same driver and owned cache; it drives a self-contained
component fixture and real routes from the production static build. It claims
Chromium behavior in the primary gate; an optional Firefox/WebKit smoke reuses
the same locked driver and owned cache but is not full cross-engine coverage.
