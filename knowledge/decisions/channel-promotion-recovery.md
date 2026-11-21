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
- The active receipt requires the docs commit and digest to match the observed
  multi-architecture image in each registry. Stable renders its canonical and
  immutable routes with the release version before deployment. Nightly retains
  its verified live identity. Both channels' rendered OpenAPI versions are
  checked, and an off-path HTTPS probe precedes the canonical deployment.
- When a Stable alias is absent, establish it at the already-public release
  verified against its GitHub manifest, both latest tags and the live docs.
  Re-read both registries before filling each absent alias. An interrupted
  setup resumes the missing alias; a conflicting present tag blocks the move.
  The candidate transaction then has an observed previous digest for every alias.
- Release downloads are staged and checksum-verified on a private draft before
  channel changes. A trusted interrupted-run journal binds the private release
  ID, prior deployment and retained Pages archives. Recovery requires the same
  private draft. The minor alias advances only after public download verification.
- Stable requires observed dual-registry digests and trusted gate commands.
- All channel and docs moves share ONE non-cancelling `pages-deploy` lock; no
  nested group. Site-only dispatches keep their active receipt. A trusted
  `nightly=true` dispatch uses the full Nightly transaction. A partial
  move converges, a stale/out-of-order candidate is refused, and an idempotent
  alias at the target is a no-op. No immutable tag is overwritten or deleted.

# Read next

- [Channel promotion gates](channel-promotion.md)
- [Release](../workflows/release.md)
