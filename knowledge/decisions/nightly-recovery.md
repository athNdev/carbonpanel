---
type: Decision
title: "Split Nightly recovery"
description: Recover a Hub-only Nightly alias and separate docs baseline with digest-bound compensation and durable phase replay.
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows/cd.yml
  - scripts/nightly_recovery.py
  - scripts/test_nightly_recovery.py
sources:
  - title: Nightly recovery runbook
    resource: ../../docs/nightly-recovery.md
---

# Decision

- When GHCR Nightly is definitively absent, retain independently verified Hub,
  GHCR absence and exact deployed Pages bytes. This state cannot authorize the
  regular dual-registry rollback path.
- A reviewed plan contains the actual prior, staged and final trees and the
  qualified immutable image digests, platform provenance and main CI. Stage only
  a non-canonical candidate path before moving Hub, then deploy and probe
  canonical HTML and OpenAPI before creating GHCR Nightly.
- Both docs builds consume the verified public Stable release identity. Recovery
  stops if that identity or its registry evidence changes. Each rendered build
  must pass `scripts/check_website.py` with the exact pinned identities before a
  staged write; authored cross-page links must resolve under the pinned route
  (site-root paths are rebased by the renderer, while a lone `../` on a nested
  page resolves inside its own directory and fails the check).
- Replay one explicitly selected mutation under the shared Pages lock. Observe
  its exact pre-state or post-state. Keep all immutable references. Before GHCR
  creation the prior Hub and exact Pages can be restored independently. Creation
  is the commit point because deleting a package version can remove the immutable
  tag. A final receipt requires observed dual-registry and canonical byte parity.

# Read next

- [Channel promotion recovery and serialization](../decisions/channel-promotion-recovery.md)
- [Release](../workflows/release.md)
