---
type: Decision
title: Release staging keeps the raw image build context
description: The trusted staging run publishes both the OCI archive and the raw build context because the Compose matrix consumes the context.
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows/release.yml
  - scripts/qualify-compose-artifacts.sh
sources:
  - title: Channel promotion decision
    resource: ../../docs/adr/0052-channel-promotion-gates.md
  - title: Release operations and versioning
    resource: ../../docs/releases.md
---

# Decision

- The `image` job of the trusted release workflow publishes `verified-release-oci`
  (OCI archive plus digest) AND `release-image-context` (the raw `dist/image`
  Docker build context) from the same successful staging run.
- `scripts/qualify-compose-artifacts.sh` builds the candidate image from the raw
  context and byte-compares its binaries with the release bundle. Replacing that
  context with only the OCI archive breaks the final Compose upgrade/rollback
  matrix, which pins a staging run by ID, SHA and digest.
- This is a compatibility contract between two owned surfaces, not a second
  publication path: no registry, tag, alias or Pages write is added.

# Read next

- [Channel promotion recovery](channel-promotion-recovery.md)
- [Release](../workflows/release.md)
