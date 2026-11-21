---
type: Decision
title: Docker Hub description ownership
description: A dedicated main and scheduled workflow derives a byte-bounded Docker-focused body from README and the published version contract; image publication never writes it.
status: stable
owners: [oss-runtime]
code_refs: [.github/workflows/dockerhub-description.yml, .github/workflows/cd.yml, .github/workflows/release.yml, docs/dockerhub-description.json, docs/version-index.json, scripts/dockerhub_description.py, scripts/test_dockerhub_description.py]
sources:
  - title: Release operations and versioning
    resource: ../../docs/releases.md
---

The Docker Hub `full_description` for `atharvnaphade/carbonpanel` is owned by
`.github/workflows/dockerhub-description.yml`, never by image publication.

- **Choice:** derive a deterministic, Docker-focused body from selected README
  sections plus the published Stable identity in `docs/version-index.json`;
  rewrite relative links to absolute URLs and fail closed above 25,000 UTF-8 bytes.
- **Alternatives rejected:** syncing raw `README.md` from CD/release (26,166 bytes,
  truncated and stale — OSS-431), and duplicating the Stable version in the
  generator config (a promotion could leave an obsolete release on the public page).
- **Consequence:** CD and release do not write the description. The dedicated
  workflow reconciles on main pushes, a daily schedule and manual recovery, and
  fails CI unless a public Docker Hub read-back matches the generated body byte
  for byte. Production generation fails closed when the live published index is
  unavailable or older than the checked-in baseline: neither a docs outage nor
  a pre-promotion identity may be advertised. A main-head guard prevents a
  stale scheduled run from reverting a newly promoted Stable. Only the resolver
  steps use the workflow's read-only GitHub token so these ref lookups avoid the
  shared unauthenticated rate limit. Before any write, production must also
  prove that identity against a public, final Stable release manifest and the
  verified `:latest` digest/revision in both registries; on disagreement or an
  unavailable provider it preserves the last published body.
