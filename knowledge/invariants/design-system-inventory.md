---
type: Invariant
title: Design-system component inventory
description: Every component exported from carbon/index.ts is documented with a prop table and behaviour notes on the design-system page.
status: stable
owners: [oss-runtime]
code_refs:
  - docs/design-system
  - web/carbon-panel/src/lib/components/carbon/index.ts
  - scripts/test_design_system_page.py
sources:
  - title: Design-system guide
    resource: ../../docs/design-system/README.md
verified: { by: process:scripts/test_design_system_page.py, at: 2026-11-07 }
review_after: 2027-05-07
---

# Rule

Every named export in `web/carbon-panel/src/lib/components/carbon/index.ts`
has a section in `docs/design-system/index.html` with the anchor
`c-<kebab-name>`, a prop table and behaviour notes. Adding, renaming or removing
an export updates the page and `scripts/test_design_system_page.py` in the same
change.

# Why

The page is the only place where the shared component surface is described in
one place. A partial inventory is worse than none: it implies a component does
not exist or that an undocumented prop is unsupported. The regression test
derives its list from the export file, so the page cannot drift silently.

# Verification

`python -m unittest scripts.test_design_system_page` fails when an export has no
matching section. Visual and behavioural accuracy is reviewed by hand against
the component source; the test only enforces coverage.
