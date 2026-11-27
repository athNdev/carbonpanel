---
type: Decision
title: Channel promotion recovery and serialization
description: Prior-receipt rollback, exact docs restore, shared lock and target guard for channel promotion.
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows/cd.yml
  - .github/workflows/release.yml
  - .github/workflows/stable-recover.yml
  - scripts/stable_recovery.py
  - scripts/release.py
  - docs/adr/0052-channel-promotion-gates.md
sources:
  - title: Channel promotion decision
    resource: ../../docs/adr/0052-channel-promotion-gates.md
  - title: Release operations and versioning
    resource: ../../docs/releases.md
---

# Decision

- Rollback uses the separately VERIFIED prior receipt captured before any write,
  never the candidate receipt returned by the commit decision. It runs under
  `always()` with explicit outcome checks and no commit-decision success guard,
  so a failed deploy or probe can still trigger it.
- Rollback restores BOTH registry aliases and the EXACT prior docs BYTES from a
  distinct Pages artifact; a re-stamp cannot restore byte identity. The public
  docs byte hash is re-checked. Stable consumes native archives and the OCI
  index from the successful trusted staging run pinned by ID, SHA and digest.
- No legacy alias-write step remains: Nightly and Stable aliases move ONLY in
  the qualified transaction. Stable binds the exact target version to both
  registry digests and the deployed public docs marker, and GitHub release
  creation is gated on that synchronized transaction. A failed stable alias
  write is compensated by restoring aliases and docs to the prior receipt.
- The active receipt requires docs commit and digest to match each registry's
  observed multi-architecture image. Stable renders canonical and immutable
  routes with the release version; Nightly retains its verified live identity.
  Both rendered OpenAPI versions are checked, and an off-path HTTPS probe precedes deployment.
- When a Stable alias is absent, establish it at the already-public release
  verified against its GitHub manifest, both latest tags and the live docs.
  Re-read both registries before filling each absent alias. An interrupted
  setup resumes the missing alias; a conflicting present tag blocks the move.
  The candidate transaction then has an observed previous digest for every alias.
- Release downloads are staged and checksum-verified on a private draft before
  channel changes. A trusted interrupted-run journal binds the private release
  ID, prior deployment and retained Pages archives. Recovery requires the same
  private draft; Stable also needs observed dual-registry digests and trusted gate commands.
- All channel and docs moves share ONE non-cancelling `pages-deploy` lock with
  `queue: max`; no nested group. Nightly keeps its deployed Pages bytes and
  binds alias moves to the verified prior receipt; site-only dispatches keep
  their active receipt, and a trusted `nightly=true` dispatch can advance Nightly.
  Publishers revalidate live source and artifacts under the lock; partial or stale
  work converges or is refused, idempotent aliases are no-ops, and immutable tags
  are never overwritten or deleted.

# Read next

- [Channel promotion gates](channel-promotion.md)
- [Release](../workflows/release.md)
