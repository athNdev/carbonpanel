---
type: Workflow
title: Release
description: The checks and artifacts required before a tagged Carbon Panel release.
status: stable
owners: [oss-runtime]
code_refs: [.github/workflows, Makefile, CHANGELOG.md, scripts/release.py, scripts/go_mod_download.py, scripts/upgrade_rehearsal_ci.py, .github/workflows/upgrade-rehearsal.yml, internal/buildinfo, docker/Dockerfile.release, docker/Dockerfile.carbon-panel]
sources:
  - title: Continuous integration
    resource: ../../.github/workflows/ci.yml
  - title: Release operations and versioning
    resource: ../../docs/releases.md
  - title: Release artifact decision
    resource: ../../docs/adr/0010-versioned-release-artifacts.md
  - title: Publication boundary and version domains
    resource: ../../docs/adr/0033-release-tag-and-publication-policy.md
  - title: Retrospective tag classification and product tag migration
    resource: ../../docs/release-history.md
  - title: Disposable real-binary upgrade rehearsal decision
    resource: ../../docs/adr/0044-disposable-upgrade-rehearsal-ci.md
  - title: Verified runtime artifact installation
    resource: ../../docs/node-runtime/verified-artifacts-install.md
---

# Parts

- [Release — Steps](release-steps.md) — Update CHANGELOG.md and any user-facing docs affected by the change.
- [Release — Rules](release-rules.md) — Publication is an explicit opt-in enforced by publication_allowed() in
- [Release — Rules (part 2)](release-rules-2.md) — the exact mod-vMAJOR.MINOR.PATCH ref; build uses git show-ref --verify,
- [Release — Rules (part 3)](release-rules-3.md) — the existing edge image is not a fresh nightly.
- [Release — Rules (part 4)](release-rules-4.md) — or reclassified as a successful check.
- [Release — Read next](release-read-next.md) — Detailed guidance on read next.
