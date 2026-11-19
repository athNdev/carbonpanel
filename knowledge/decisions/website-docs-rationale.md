---
type: Decision
title: "Website and documentation publication — Rationale + Verification"
description: "Independent Pages deployments overwrite one another. A single artifact preserves"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: website-docs.md
---
# Rationale

Independent Pages deployments overwrite one another. A single artifact preserves
both routes, and an explicit source pin prevents examples and API schemas from
silently following unreleased changes.

# Verification

Use the commands and review procedure in the publishing guide. Snapshot-isolation
tests verify that working-tree edits cannot leak into pinned docs or examples.
Serve `landing/qa-closing-layout.html` for browser checks of the actual closing
banner from 320px through 3440px, including desktop title and link wrapping.
