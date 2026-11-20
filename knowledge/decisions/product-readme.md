---
type: Decision
title: README artwork and product captures
description: Separate the README's decorative brand artwork from accurate, focused captures of the current application.
status: stable
owners: [oss-runtime]
code_refs:
  - README.md
  - assets/readme
  - assets/showcase
  - landing/assets/showcase
  - scripts/review/plan-showcase.json
  - scripts/review/journey.mjs
  - scripts/review/entry-state.mjs
  - scripts/showcase_assets.py
  - .github/workflows/showcase.yml
sources:
  - title: Capture provenance and regeneration
    resource: ../../assets/showcase/README.md
  - title: README artwork source
    resource: ../../assets/readme/README.md
---

# Decision

The root README carries the website's terrain and approved logo in a decorative
banner, with portable HTML source beside its raster export. The terrain keeps
its natural 3:2 proportions; close framing is achieved by cropping the composition
rather than stretching the artwork. Product screenshots are a separate kind of
asset: native browser captures of the current application
running in an isolated installation with review data. Never substitute generated
UI, the homepage's fixture islands or edited telemetry for an application capture.

Use focused crops and native UI controls to make the represented feature readable.
Do not repaint labels, invent activity or hide a missing reading behind a plausible
metric. Capture provenance records the application revision, date, data origin and
framing. Review exports at README display size and refresh them when that UI changes.
The inline dashboard is a native-width content crop. Link it to the original
full-window PNG so readers can inspect the surrounding shell at native scale.

The review stack uses its own fixtures and preserves data on shutdown. The
Players capture requires a scoped observation from a headless game client
connected to the fresh review server on a verified loopback binding.
The journey targets stable tab and panel IDs and pins Bun to the frontend CI
version. Relevant main pushes run capture automatically, with a weekly backstop
for missed changes. Default dispatch includes the full set. Subset and non-main
dispatches produce a validated artifact for inspection.

Published images live on a dedicated asset branch with stable README raw URLs.
Capture runs in a read-only job against an isolated seeded stack. A separate
minimal `contents:write` job publishes the canonical bundle atomically after
validation (exact source commit, per-image SHA256, dimensions, size, PNG
chunk CRCs, alt text, provenance, no credential material or unexpected files).
Publication advances along live main history. A later merge may land during
capture without invalidating it, and an older, repeated or unchanged capture
leaves the last published assets intact. Login-route bootstrap waits for one
mutually exclusive semantic state before capturing.
The gated World View capture has its own fail-closed rule: see
[World View capture evidence](world-view-capture-evidence.md).
