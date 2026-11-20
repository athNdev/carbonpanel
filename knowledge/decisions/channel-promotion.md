---
type: Decision
title: Channel promotion gates
description: Exact-revision, dual-registry and qualified-stage gates for docs and OCI channel moves.
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows/cd.yml
  - .github/workflows/landing.yml
  - scripts/release.py
  - docs/adr/0052-channel-promotion-gates.md
sources:
  - title: Channel promotion decision
    resource: ../../docs/adr/0052-channel-promotion-gates.md
  - title: Release operations and versioning
    resource: ../../docs/releases.md
---

# Decision

- A docs or OCI channel move needs an EXACT source revision pinned read-only
  from trusted `refs/heads/main`, a full 40-hex commit, and a successful
  exact-commit main CI result. A tag ref, short SHA or unauthenticated CI lookup
  fails closed.
- OCI qualification requires the immutable `sha-<commit>` image at ONE verified
  multiarch digest with both Linux architectures in BOTH maintained registries
  (Docker Hub `atharvnaphade/carbonpanel` and GHCR `ghcr.io/athndev/carbonpanel`),
  with matching OCI revision in each digest-addressed child config. A missing,
  unknown, single-arch or mismatched registry is a zero-write refusal.
- Docs qualification requires a schema_version 3 two-channel contract (`Stable`
  and `Nightly` only, no legacy doc snapshots, full-SHA pinned links) from the
  built site and exact site build validation. Stale docs never mask a candidate.
- A move is ONE qualified transaction: reject older candidates against the
  active receipt, stage both immutable registries and the exact docs artifact,
  confirm a public candidate marker at the non-active `/docs/candidate/<sha>/`
  route before any alias move, then confirm both registries and the live marker
  agree. A receipt records revision, digests, before/after aliases, docs hash,
  marker URLs, run IDs and timestamps.
- `nightly` advances on each successful main on BOTH registries at one digest
  with NO GitHub Release and no redundant `rolling` channel. Its revision lives
  in the published artifact, marker and receipt, never a tracked main file that
  would self-trigger CI. Canonical `carbonpanel-v*` images stay immutable.
- The Hub-only nightly baseline is not a mandatory first revision; it seeds only
  through a recorded bounded bootstrap with dual-registry parity and a matching
  docs marker. No shipped workflow supplies `--bootstrap`.
- `latest` and `stable` move only from the verified stable digest; the occupied
  `carbonpanel-v2.0.0` and arbitrary/legacy tags never publish.

# Read next

- [Channel promotion recovery](channel-promotion-recovery.md)
- [Release](../workflows/release.md)
