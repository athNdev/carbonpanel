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
sources:
  - title: Capture provenance and regeneration
    resource: ../../assets/showcase/README.md
  - title: README artwork source
    resource: ../../assets/readme/README.md
---

# Decision

The root README carries the website's terrain and approved logo in a decorative
banner, with portable HTML source beside its raster export. Product screenshots
are a separate kind of asset: native browser captures of the current application
running in an isolated installation with review data. Never substitute generated
UI, the homepage's fixture islands or edited telemetry for an application capture.

Use focused crops and native UI controls to make the represented feature readable.
Do not repaint labels, invent activity or hide a missing reading behind a plausible
metric. Capture provenance records the application revision, date, data origin and
framing. Review exports at README display size and refresh them when that UI changes.

`assets/showcase/` and `landing/assets/showcase/` contain identical PNGs. The standard
journey maintains the core captures; additional manual feature captures have explicit
regeneration instructions. Local review stacks preserve worlds and databases during
shutdown, and never borrow another installation's data for a public screenshot.
