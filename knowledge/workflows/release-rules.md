---
type: Workflow
title: "Release — Rules"
description: "Publication is an explicit opt-in enforced by publication_allowed() in"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: release.md
---
# Rules

- Publication is an explicit opt-in enforced by `publication_allowed()` in
  `scripts/release.py`. Branch pushes, tag pushes, PRs, CI, documentation and
  rehearsal tags only stage, build and verify; no registry or release write.
  `workflow_dispatch` with `publish=true`, a canonical `carbonpanel-v*` tag, a
  trusted `refs/heads/main` ref and exact-commit main CI (verified by
  `verify-publication-ref`) is the only panel publication path. Inherited,
  source-only, preview, name-collision and unclassified tags are rejected. The
  Git tag, displayed SemVer, immutable image tag and archive filename are
  separate fields; product tags are never parsed as Go versions. Plain `v*` and
  `mod-*` tags stay in their historical domains. The 84-tag classification with
  domain and provenance is in `docs/release-history.md` and
  `docs/release-history.json`; `v1.0.40` is the only verified historical stable
  and the current 1.0 rollback line (not a permanent ban on 1.0.x maintenance).
  The planned `carbonpanel-v1.0.40` alias is historical-only and refused as a
  new release candidate before registry preflight.
  The ENTIRE explicit publication run holds one workflow-level non-cancelling
  `carbonpanel-product-publish` concurrency group across immutable publish,
  GitHub release and alias promotion; build-only rehearsals and ordinary CI use
  a distinct per-run key and are not serialized. `cancel-in-progress: false`
  protects the running run only; default `queue: single` can replace the pending
  run, so this is single-operator dispatch and the operator must verify the
  resulting run before retry. GitHub `latest` follows the same product-only
  verified stable ordering as registry aliases.
  Staging a tag makes no claim about its target's age: tags can point at old
  commits and tag-triggered runs reflect that history. The dispatch revision is
  a convenience, not the trusted gate; the Python boundary plus exact-commit
  main CI are authoritative.
- Every step that calls `gh` — directly or through a gh-backed `scripts/release.py`
  gate (including `plan`, `preflight-immutable` on the resume path,
  `verify-publication-ref`, `verify-main-ci`, `release-record`, `publish`,
  `verify-release-record`, `alias-promotion` and `promote-alias`) — sets `GH_TOKEN`
  in its own step `env`. gh does not inherit a token set on a different
  step or job; a missing token makes the gate fail closed. `verify-publication-ref`
  in `release.yml` is the trusted-ref/exact-CI boundary and must carry it.
  `scripts/workflow-contract.test.mjs` guards this for both publication lanes.
- Publication is phased. A read-only preflight classifies each registry
  reference as absent, present or unknown; conflicts, unknown errors and partial
  states refuse. The immutable full-version tag is pushed and verified
  (tag→digest plus both Linux architectures in both registries) before any
  alias moves. Aliases are promoted in separate, serialized, re-verified steps,
  monotonically against published stable SemVer: no moving backwards, no
  promotion from older versions, no `latest` from prereleases. An alias whose
  ordering or immutability cannot be established is omitted with a recorded
  reason rather than guessed. RESUME requires more than identical tag digests:
  an operator-supplied verified digest, exact tag-to-commit identity and a
  release manifest whose version, commit, revision and digest all match;
  unknown or mismatched provenance refuses. Module images use the same split and
