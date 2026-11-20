---
type: Decision
title: Channel promotion recovery and serialization
description: Prior-receipt rollback, exact docs restore, shared lock and target guard for channel promotion.
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows/cd.yml
  - .github/workflows/release.yml
  - scripts/release.py
  - docs/adr/0049-channel-promotion-gates.md
sources:
  - title: Channel promotion decision
    resource: ../../docs/adr/0049-channel-promotion-gates.md
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
  docs byte hash is re-checked. Retry resumes the exact recorded digest and
  never rebuilds a different image.
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
- The stable gate consumes OBSERVED dual-registry digests, not a hardcoded
  literal. An older release target whose checkout lacks the trusted gate script
  is refused with an explicit blocker rather than silently using the wrong code.
- All channel and docs moves share ONE non-cancelling `pages-deploy` lock; no
  nested group and no dispatched workflow takes the same lock. A manual/site-only
  dispatch publishes only an already-active source from the receipt. A partial
  move converges, a stale/out-of-order candidate is refused, and an idempotent
  alias at the target is a no-op. No immutable tag is overwritten or deleted.
- `verify-publication-ref` supplies `GH_TOKEN` so the fail-closed exact-CI check
  can pass; this enables the check rather than bypassing it.

# Read next

- [Channel promotion gates](channel-promotion.md)
- [Release](../workflows/release.md)
