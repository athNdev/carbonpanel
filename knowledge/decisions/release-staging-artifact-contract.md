---
type: Decision
title: Release qualification uses the retained OCI archive
description: The Compose upgrade matrix uses the original verified OCI digest from a trusted Stable staging run and supports older raw build contexts.
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

- `scripts/qualify-compose-artifacts.sh` validates the trusted staging receipt
  and compares the OCI index hash with its recorded digest. The loopback registry
  receives the complete index without changing its digest. Compare its raw index
  to the archive before exercising clean install, upgrade and rollback.
- A legacy stage without a receipt can supply the raw `release-image-context`.
  Byte-compare both Linux binaries with the release bundle and record the rebuilt
  candidate as a separate image.
- The fixture writes only to the disposable runner's loopback registry.

# Read next

- [Channel promotion recovery](channel-promotion-recovery.md)
- [Release](../workflows/release.md)
