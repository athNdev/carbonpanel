# ADR 0049: Verified channel promotion gates for docs and OCI

- Status: Accepted
- Date: 2026-11-20
- Owners: oss-runtime
- Relates to: ADR 0033 (release tag and publication policy), ADR 0041
  (bounded trusted-main nightly), ADR 0045 (version contract and multi-version
  docs), OSS-413

## Context

Moving-channel aliases (`latest`, a `stable` alias, `nightly`, a new `rolling`
image) and the public documentation switcher have been updated by separate,
manual paths. That allows three failure modes: a channel can point at a revision
whose CI never passed, the docs selector can advertise a channel whose image was
never verified, and a partially applied move can leave one registry or route at
a newer digest than the other. The existing `nightly` publisher was Docker Hub
only and required an explicit dispatch, so it could go stale. The `v1.0.40`
stable baseline (`sha256:ead260ef…`, commit `f88748be…`) and the current Docker
Hub nightly baseline (`sha256:ecc89ba9…`, commit `d8597d4e…`) are verified facts
about today's registries, not a licence to relabel an old digest as a new
channel candidate.

The authoritative OSS-413 contract (`oss413-channel-contract.md`, dated
2026-11-20) requires two disjoint owners: Owner A builds a schema_version 3
two-channel docs contract; Owner B (this ADR) gates every docs and OCI channel
move. No implementation step may dispatch a workflow, write a registry, create a
release, change a tag or deploy Pages.

Primary-source research is consolidated in the dated note
`container-image-stable-rolling-channels-2026-11-20.md` (in the research
workspace). It is the only research evidence used here; selected excerpts are
paraphrased below rather than duplicated:

- Docker's GitHub Actions introduction and tags/labels guide show branch/tag/
  schedule triggers and `docker/metadata-action` tag patterns, with publishing
  gated to non-PR events (`https://docs.docker.com/build/ci/github-actions/`
  and `https://docs.docker.com/build/ci/github-actions/manage-tags-labels/`).
  These are example trigger patterns, not a universal policy.
- GitHub's "Publishing Docker images" page shows release-published and
  release-branch-push triggers, supporting per-release updates for stable
  channels (`https://docs.github.com/en/actions/publishing-packages/publishing-docker-images`).
- Grafana documents `grafana/grafana:main` as a **moving** tag refreshed after
  each successful main build, alongside immutable per-build `grafana-dev`
  tags, and recommends pinning immutable tags for reproducibility
  (`https://grafana.com/docs/grafana/latest/setup-grafana/configure-docker/`).
  This configure-docker pattern informs the user-facing `nightly` alias: a
  moving tag refreshed on green main, with per-commit `sha-*` images immutable
  for pinning. No separate rolling channel is introduced.
- Docker documents `latest` as the omitted-tag default and an ease-of-use
  convention, not an implicit stability contract; documentation must state
  what it tracks (`https://docs.docker.com/reference/cli/docker/image/tag/`,
  `https://docs.docker.com/docker-hub/image-library/trusted-content/`).

## Decision

1. **One contract, two channels.** The docs build publishes a schema_version 3
   artifact with exactly `stable` and `nightly` (public labels `Stable` and
   `Nightly`, no version terminology). Both carry a full 40-hex commit and a
   verified sha256 digest whose revision equals that commit. Stable pins
   `latest` plus a `stable` alias from the verified stable digest; nightly has
   no version tag. Legacy doc snapshots `1.0.39` and `3.0.0-alpha.1` are removed
   from the build/selector while their source tags and historical records stay
   in `docs/release-history.json`. Pinned links use `${repository}/commit/<SHA>`.
2. **Exact source revision + qualified stage, for both lanes.** A move requires
   a full-SHA revision pinned read-only from trusted `refs/heads/main` and a
   successful exact-commit main CI check. OCI additionally requires the
   immutable `sha-<commit>` image to be verified at one digest with both Linux
   architectures **in both maintained registries** (Docker Hub
   `atharvnaphade/carbonpanel` and GHCR `ghcr.io/athndev/carbonpanel`), each
   child config carrying the same OCI revision. Docs additionally require the
   built contract to validate and pin that same revision plus exact site build
   validation, so stale docs can never mask a candidate. A tag ref, a short SHA,
   an unknown registry, a single-arch image or a mismatched revision fails
   closed with zero writes.
3. **One qualified transaction per channel.** A move is not an alias write:
   pin the exact revision and qualified stage, reject older/out-of-order
   candidates against the active receipt, stage BOTH immutable registries and
   the exact docs artifact, deploy a candidate docs build and confirm its PUBLIC
   marker over HTTPS before any alias move, then deploy final docs and confirm
   both registry aliases and the live marker/dropdown agree. A full receipt
   records revision, both digests, before/after alias state, docs artifact hash,
   public marker URLs/status, run IDs and timestamps. Retry reuses the exact
   staged digest (never a rebuild or blind alias move); partial failure
   reconciles or rolls back both surfaces to the prior verified receipt.
   `nightly` advances on each successful trusted main CI on **both** registries
   at the same digest and OCI revision and creates NO GitHub Release; per-commit
   `sha-*` images stay immutable. No redundant `rolling` channel is added: the
   user-facing `nightly` alias and existing release aliases are sufficient.
   Nightly revision belongs in the published artifact/marker and receipt only —
   it is never committed to a tracked main file, which would self-trigger CI.
   A reconciliation schedule re-verifies and repairs drift.
4. **Bounded bootstrap, never a blind relabel.** `d8597d4e…`/`sha256:ecc89ba9…`
   is a verified current Docker Hub baseline, not a mandatory first revision.
   It seeds nightly only through an explicit `--bootstrap` whose dual-registry
   parity and matching docs marker are re-verified; the normal path promotes the
   first qualified current-main revision. No code path supplies `--bootstrap`.
5. **One lock, retry and rollback.** All channel and docs moves share ONE fixed
   non-cancelling publication lock (`pages-deploy`, `cancel-in-progress: false`).
   No job nests a second group and no workflow dispatches another publisher that
   takes the same lock, so there is no deadlock. A manual/site-only
   `landing.yml deploy=true` dispatch may publish ONLY an already-active exact
   channel source reconstructed from the live receipt; it never originates a new
   promotion. Each move is digest-anchored and bound to the previously verified
   digest and commit; an ancestor check refuses stale/out-of-order revisions, an
   already-correct alias is an idempotent no-op, a partial move converges, and a
   candidate that loses qualification rolls back to the previous verified
   receipt. Immutable version tags are never overwritten and no tag is deleted.
   No publication occurs from arbitrary, inherited or legacy tags; the planned
   `carbonpanel-v2.0.0` stable is not a channel publication. A registry-only PR
   without a synchronized docs deploy and receipt remains opt-off staging and
   does not complete OSS-413.
6. **Fail-closed ref check repaired.** The `verify-publication-ref` step of
   `release.yml` now supplies `GH_TOKEN`; without it `gh run list` is
   unauthenticated and the exact-CI check could never pass. This enables the
   check rather than bypassing it.
7. **No legacy alias-write bypass.** Every former `nightly-promote`,
   `edge-promote`, independent stable-alias and bare `latest` write step was
   removed from the workflows. Both Nightly and Stable aliases move ONLY inside
   the qualified transaction. Stable binds the exact target product version and
   the deployed public docs marker together, and GitHub release creation is
   gated on that synchronized transaction; a stale local manifest is QA-only.
8. **Genuine failure branch.** Rollback runs under `always()` with explicit
   outcome checks and NO `commit_decision` success guard (a failed deploy/probe
   skips that step). The receipt it uses is the separately VERIFIED prior
   receipt captured in phase 1 before any write, never the candidate receipt
   returned by the commit decision. Rollback restores BOTH aliases and the
   EXACT prior docs BYTES from a distinct Pages artifact; a re-stamp cannot
   restore byte identity. Retry resumes that exact digest and never rebuilds.
9. **Gate evidence and target guard.** The stable transaction validates the
   OBSERVED digests and revisions in both registries and feeds them into the
   gate; no hardcoded digest is accepted as evidence. A failed dual-registry
   stable alias write is compensated by restoring the prior aliases and docs.
   Both release jobs refuse an older release target whose checked-out
   `scripts/release.py` lacks the trusted gate commands, instead of silently
   using the wrong script. Offline injection covers a second-registry write
   failure, final-probe failure, prior-receipt rollback, bounded bootstrap,
   an older target and a prerelease candidate.

## Consequences

A channel now represents a verified pairing of source revision, CI result,
multiarch digest (both registries) and docs marker. Retrying a failed deploy is
safe, rollback preserves the last verified digest, and no automatic event can
manufacture a release or retarget an immutable tag. The registry writes and
Pages deployment required for live parity are deliberately outside this change:
they are executed only after independent exact-head review and green CI.

## Verification

Offline fixtures in `scripts/test_release.py` cover first publish, retry after a
failed deploy, stale/out-of-order revisions, partial-failure rollback preserving
the previous verified digest, dual-registry digest parity, arbitrary/legacy tag
refusal, bounded bootstrap, and CLI/workflow wiring with no registry, release,
tag or Pages write. Python unit tests, YAML parse, Go test/vet, knowledge lint
and `git diff --check` are run before opening the draft PR.
