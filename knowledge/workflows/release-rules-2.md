---
type: Workflow
title: "Release — Rules (part 2)"
description: "the exact mod-vMAJOR.MINOR.PATCH ref; build uses git show-ref --verify,"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: release.md
---
  the exact `mod-vMAJOR.MINOR.PATCH` ref; build uses `git show-ref --verify`,
  never a glob existence check. The durable release record completes before any
  alias moves; each move re-checks the actual registry alias digest against
  durable digest-to-version evidence and refuses on uncertainty. Main `edge` and
  Pages effects use the same gate: an ordinary verified merge builds and
  validates only; publication requires a trusted `refs/heads/main` dispatch with
  the channel opt-in and a read-only exact-commit main-CI check (`verify-main-ci`)
  for a PINNED commit. Manual dispatch checks out the event SHA and workflow_run
  checks out the CI run's head SHA; full history is fetched for ancestry and a
  step asserts the actual HEAD equals that SHA before any side-effecting step.
  Tags, labels and provenance use the same SHA, never a moving branch tip.
  Verification uses per-SHA cancellable concurrency groups; opted-in
  publication uses a fixed channel-wide non-cancelling group (`cd-publish-edge`
  / `pages-deploy`) with no SHA, so publishes of different commits serialize on
  the moving alias and routine builds cannot cancel an in-flight write. An
  operator checkbox is never CI evidence. IndexNow notification is deferred
  because URLs come from a push diff while deployment is dispatch-only. A scoped
  publish-boundary opt-in needs release-auditor review before held merges.
- Human-readable `build-YYYY-MM-DD-HHMMSS` aliases are immutable additions at
  exact existing digests, not releases or channels, and carry no SHA fragment.
  They are recomputed from the checked-in mapping by
  `scripts/docker_readable_aliases.py` using observed Hub tag publication time
  (`tag_last_pushed`, fallback `last_updated`; a proxy, not build time) and never
  move `latest`, a minor tag, `edge`, `nightly` or an original `sha-*` ref. Only
  an explicit trusted-main dispatch may add them, using imagetools create with
  strict observation and idempotence; see ADR 0039.
- `nightly` is a development alias, not a release version: it points to the most
  recent qualifying build of a pinned main commit with exact successful CI, and
  reuses the verified `sha-<full-commit>` image rather than adding a date alias.
  Both edge and nightly writers preflight that shared sha tag before any build
  or push: strict lookup classification treats only a definitive manifest-unknown
  as absent, and a missing tag with an expected digest refuses. Reuse needs
  independent pinned-build evidence — exact successful CI for the commit, digest
  match, both platform child manifests and matching `org.opencontainers.image.revision`
  in each digest-addressed child config (not descriptor annotations); a bare
  expected digest is not evidence. Every evidence fetch is anchored to the
  observed digest (`repo@digest`) and the reported index digest must match or
  publication refuses with zero writes. Not-found classification is scoped to
  the queried reference. Edge uses separate previous binding inputs. Nightly and edge promotion bind the previous
  commit to the observed alias digest through that same evidence before checking
  ancestry. Buildx writes only the sha reference; an explicitly gated digest-based
  step moves edge so it survives sha reuse in either order. Nightly never moves
  `latest`, a minor tag or `edge`. A native Actions schedule (18:00 UTC) may now
  publish nightly only from the exact current main tip, with a late main/CI
  recheck before the buildx push and another before the alias move. It verifies
  by default and writes only when the separately reviewed, default-off
  `NIGHTLY_SCHEDULE_PUBLISH` variable is true. It joins the fixed channel-wide
  publish concurrency group and adds no schedule to the release, module, MCP or
  runtime lanes. See ADR 0041. Seeding a live nightly is separately authorized;
