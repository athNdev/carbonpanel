---
type: Decision
title: Channel promotion gates
description: Exact-revision, dual-registry and qualified-stage gates for docs and OCI channel moves.
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows/cd.yml
  - .github/workflows/landing.yml
  - .github/workflows/release.yml
  - scripts/release.py
  - scripts/test_release.py
  - docs/adr/0049-channel-promotion-gates.md
sources:
  - title: Channel promotion decision
    resource: ../../docs/adr/0049-channel-promotion-gates.md
  - title: Release operations and versioning
    resource: ../../docs/releases.md
  - title: Website publishing
    resource: ../../docs/website-publishing.md
---

# Decision

- A docs or OCI channel move needs an EXACT source revision pinned read-only
  from trusted `refs/heads/main`, a full 40-hex commit, and a successful
  exact-commit main CI result. A tag ref, a short SHA or an unauthenticated CI
  lookup fails closed.
- OCI qualification also requires the immutable `sha-<commit>` image at ONE
  verified multiarch digest with both Linux architectures in BOTH maintained
  registries (Docker Hub `atharvnaphade/carbonpanel` and GHCR
  `ghcr.io/athndev/carbonpanel`), with matching OCI revision in each digest-
  addressed child config. A missing, unknown, single-arch or mismatched
  registry is a zero-write refusal.
- Docs qualification also requires a schema_version 3 two-channel contract
  (`Stable` and `Nightly` only, no legacy doc snapshots, full-SHA pinned links)
  from the built site and exact site build validation. Stale docs never mask a
  channel candidate.
- A move is ONE qualified transaction: pin the exact revision, reject older
  candidates against the active receipt, stage both immutable registries and the
  exact docs artifact, confirm a PUBLIC candidate marker over HTTPS before moving
  aliases, then deploy final docs and confirm both registries and the live marker
  agree. A receipt records revision, digests, before/after aliases, docs hash,
  marker URLs, run IDs and timestamps; retry reuses the staged digest and partial
  failure reconciles or rolls back to the prior verified receipt.
- `nightly` advances on each successful main on BOTH registries at one digest with
  NO GitHub Release; a reconciliation re-verifies and repairs drift. No redundant
  `rolling` channel is added. Nightly revision lives in the published artifact,
  marker and receipt only â€” never in a tracked main file that would self-trigger
  CI. Canonical `carbonpanel-v*` images stay immutable.
- The verified Hub-only nightly baseline `d8597d4eâ€¦`/`sha256:ecc89ba9â€¦` is not
  a mandatory first revision. It seeds nightly only through a bounded, recorded
  bootstrap that re-verifies dual-registry parity and a matching docs marker;
  no shipped workflow supplies `--bootstrap`.
- `latest` and the `stable` alias move only from the verified stable digest
  (`f88748beâ€¦`/`sha256:ead260efâ€¦`). The planned `carbonpanel-v2.0.0` stable is
  not a channel publication; arbitrary, inherited and legacy tags never publish.
- Rollback uses the separately verified PRIOR receipt captured before any write,
  never the candidate receipt, and redeploys the EXACT prior docs bytes from a
  distinct Pages artifact (byte hash checked). The stable gate consumes OBSERVED
  dual-registry digests, compensates a failed alias write, and older release
  targets lacking the trusted gate script are refused explicitly.
- No legacy alias-write step remains: Nightly and Stable aliases move ONLY in
  the qualified transaction. Stable binds the exact target version to both
  registry digests and the deployed public docs marker, and GitHub release
  creation is gated on that synchronized transaction. Rollback runs under
  `always()` with explicit outcome checks and restores both aliases and docs
  from the exact prior receipt; retry resumes that digest without rebuilding.
- All channel and docs moves share ONE fixed non-cancelling publication lock
  (`pages-deploy`, `cancel-in-progress: false`); no nested group and no dispatched
  workflow takes the same lock. A manual/site-only dispatch may publish only an
  already-active exact source from the receipt; a registry-only PR without a
  synchronized docs deploy and receipt stays opt-off staging. Each move is
  digest-anchored and bound to the previous verified digest and commit: an
  ancestor check refuses stale/out-of-order moves, an alias already at the
  target is an idempotent no-op, a partial move converges, and a failed
  candidate rolls back to the previous verified receipt. No immutable tag is
  overwritten and no tag is deleted.
- The `verify-publication-ref` step in `release.yml` supplies `GH_TOKEN` so the
  exact-CI fail-closed check can actually pass; this repairs, not bypasses, it.

# Rationale

Separate manual publishers let a channel, the images and the docs selector
drift apart. One verified revision/digest pairing per move, serialized and
digest-bound, makes retry and rollback safe and keeps immutable releases exact.
See ADR 0049 and the dated research note referenced there.
